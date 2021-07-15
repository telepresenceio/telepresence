package userd_auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/pkg/browser"
	"golang.org/x/oauth2"

	"github.com/datawire/dlib/dcontext"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dhttp"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cache"
	"github.com/telepresenceio/telepresence/v2/pkg/client/connector/internal/scout"
	"github.com/telepresenceio/telepresence/v2/pkg/client/connector/userd_auth/authdata"
)

const (
	callbackPath = "/callback"
	apikeysFile  = "apikeys.json"
)

var ErrNotLoggedIn = errors.New("not logged in")

type oauth2Callback struct {
	Code             string
	Error            string
	ErrorDescription string
}

type loginExecutor struct {
	// static

	env              client.Env
	SaveTokenFunc    func(context.Context, *oauth2.Token) error
	SaveUserInfoFunc func(context.Context, *authdata.UserInfo) error
	OpenURLFunc      func(string) error
	stdout           io.Writer
	scout            chan<- scout.ScoutReport

	// stateful

	oauth2ConfigMu sync.RWMutex // locked unless a .Worker is running
	oauth2Config   oauth2.Config

	loginMu               sync.Mutex
	callbacks             chan oauth2Callback
	tokenSource           oauth2.TokenSource
	userInfo              *authdata.UserInfo
	apikeys               map[string]map[string]string // map[env.LoginDomain]map[apikeyDescription]apikey
	refreshTimer          *time.Timer
	refreshTimerIsStopped bool
	refreshTimerReset     chan time.Duration
}

// LoginExecutor controls the execution of a login flow
type LoginExecutor interface {
	Worker(ctx context.Context) error
	Login(ctx context.Context) error
	Logout(ctx context.Context) error
	GetToken(ctx context.Context) (string, error)
	GetAPIKey(ctx context.Context, description string) (string, error)
	GetLicense(ctx context.Context, id string) (string, string, error)
	GetUserInfo(ctx context.Context) (*authdata.UserInfo, error)
}

// NewLoginExecutor returns an instance of LoginExecutor
func NewLoginExecutor(
	env client.Env,
	saveTokenFunc func(context.Context, *oauth2.Token) error,
	saveUserInfoFunc func(context.Context, *authdata.UserInfo) error,
	openURLFunc func(string) error,
	stdout io.Writer,
	scout chan<- scout.ScoutReport,
) LoginExecutor {
	ret := &loginExecutor{
		env:              env,
		SaveTokenFunc:    saveTokenFunc,
		SaveUserInfoFunc: saveUserInfoFunc,
		OpenURLFunc:      openURLFunc,
		stdout:           stdout,
		scout:            scout,

		callbacks: make(chan oauth2Callback),
		// AFAICT, it's not possible to create a timer in a stopped state.  So we create it
		// in a running state with 1 minute left, and then immediately stop it below with
		// resetRefreshTimerUnlocked.
		refreshTimer:      time.NewTimer(1 * time.Minute),
		refreshTimerReset: make(chan time.Duration),
	}
	ret.oauth2ConfigMu.Lock()
	ret.loginMu.Lock()
	ret.resetRefreshTimerUnlocked(0)
	return ret
}

func NewStandardLoginExecutor(env client.Env, stdout io.Writer, scout chan<- scout.ScoutReport) LoginExecutor {
	return NewLoginExecutor(
		env,
		authdata.SaveTokenToUserCache,
		authdata.SaveUserInfoToUserCache,
		browser.OpenURL,
		stdout,
		scout,
	)
}

func (l *loginExecutor) tokenErrCB(ctx context.Context, err error) error {
	if retErr, isRetErr := err.(*oauth2.RetrieveError); isRetErr && retErr.Response.StatusCode/100 == 4 {
		// Treat an HTTP 4XX response from the IDP as "this token is invalid", and forget
		// about the token, at least for this run.  Don't forget about it for all runs,
		// because we'll use its validity to check whether we're logged-out or just have an
		// expired login.
		l.tokenSource = nil
	}
	return err
}

func (l *loginExecutor) tokenCB(ctx context.Context, tokenInfo *oauth2.Token) error {
	if err := l.SaveTokenFunc(ctx, tokenInfo); err != nil {
		return fmt.Errorf("could not save access token to user cache: %w", err)
	}
	l.resetRefreshTimer(time.Until(tokenInfo.Expiry))
	return nil
}

// resetRefreshTimer resets the timer to have `delta` time left on it.  If `delta` is <= 0, then it
// stops the timer.  It is safe to call resetRefreshTimer(0) on an already-stopped timer.  May only
// be called while the "refresh" goroutine is running.
func (l *loginExecutor) resetRefreshTimer(delta time.Duration) {
	// We pass this along to the "refresh" goroutine to call .Stop() and .Reset(), because the
	// time.Timer godoc tells us that this "cannot be done concurrent to other receives from the
	// Timer's channel or other calls to the Timer's Stop method."; and without doing it in the
	// refresh goroutine's main select loop, it'd be impossible to guard against concurrent
	// receives.
	l.refreshTimerReset <- delta
}

// resetRefreshTimerUnlocked is like resetRefreshTimer, but you need to be careful about not calling
// it concurrent to the "refresh" goroutine or other calls to resetRefreshTimerUnlocked (what that
// means at the moment: only call this from NewLoginExecutor() or .Worker()).
func (l *loginExecutor) resetRefreshTimerUnlocked(delta time.Duration) {
	// The timer must be stopped before we reset it.  We have to track l.refreshTimerIsStopped
	// because the <-l.refreshTimer.C receive will hang on subsequent calls (even though we're
	// checking the return value of .Stop()!).
	if !l.refreshTimerIsStopped {
		if !l.refreshTimer.Stop() {
			<-l.refreshTimer.C
		}
		l.refreshTimerIsStopped = true
	}
	// Reset the timer if delta > 0.  Leave it stopped if delta <= 0.
	if delta > 0 {
		l.refreshTimer.Reset(delta)
	}
}

func (l *loginExecutor) Worker(ctx context.Context) error {
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		return err
	}
	l.oauth2Config = oauth2.Config{
		ClientID:    l.env.LoginClientID,
		RedirectURL: fmt.Sprintf("http://localhost:%d%s", listener.Addr().(*net.TCPAddr).Port, callbackPath),
		Endpoint: oauth2.Endpoint{
			AuthURL:  l.env.LoginAuthURL,
			TokenURL: l.env.LoginTokenURL,
		},
		Scopes: []string{"openid", "profile", "email"},
	}

	l.tokenSource, err = func() (oauth2.TokenSource, error) {
		l.resetRefreshTimerUnlocked(0)
		tokenInfo, err := authdata.LoadTokenFromUserCache(ctx)
		if err != nil || tokenInfo == nil {
			return nil, err
		}
		l.resetRefreshTimerUnlocked(time.Until(tokenInfo.Expiry))
		return newTokenSource(ctx, l.oauth2Config, l.tokenCB, l.tokenErrCB, tokenInfo), nil
	}()
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	defer l.resetRefreshTimerUnlocked(0)

	l.userInfo, err = authdata.LoadUserInfoFromUserCache(ctx)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	if err := cache.LoadFromUserCache(ctx, &l.apikeys, apikeysFile); err != nil && !os.IsNotExist(err) {
		return err
	}
	if l.apikeys == nil {
		l.apikeys = make(map[string]map[string]string)
	}
	if l.apikeys[l.env.LoginDomain] == nil {
		l.apikeys[l.env.LoginDomain] = make(map[string]string)
	}

	l.oauth2ConfigMu.Unlock()
	defer l.oauth2ConfigMu.Lock()
	l.loginMu.Unlock()
	defer l.loginMu.Lock()

	grp := dgroup.NewGroup(ctx, dgroup.GroupConfig{
		EnableWithSoftness: ctx == dcontext.HardContext(ctx),
		ShutdownOnNonError: true,
	})

	grp.Go("server-http", func(ctx context.Context) error {
		sc := dhttp.ServerConfig{
			Handler: http.HandlerFunc(l.httpHandler),
		}
		return sc.Serve(ctx, listener)
	})
	grp.Go("refresh", func(ctx context.Context) error {
		for {
			select {
			case <-l.refreshTimer.C:
				dlog.Infoln(ctx, "refreshing access token...")
				if token, err := l.GetToken(ctx); err != nil {
					dlog.Infof(ctx, "could not refresh assess token: %v", err)
				} else if token != "" {
					dlog.Infof(ctx, "got new access token")
				}
			case delta := <-l.refreshTimerReset:
				l.resetRefreshTimerUnlocked(delta)
			case <-ctx.Done():
				return nil
			}
		}
	})
	return grp.Wait()
}

// Login tries logging the user by opening a browser window and authenticating against the
// configured (in `l.env`) OAuth2 authorization server using the authorization-code flow.  This
// relies on the .Worker() HTTP server being already running in the background, as it is needed to
// serve the redirection endpoint (called the "callback URL" in this code).  Once the callback URL
// is invoked, this function will receive notification of that on the l.callbacks channel, and will
// invoke the authorization server's token endpoint to get the user's access & refresh tokens and
// persist them with the l.SaveTokenFunc (which would usually write to user cache).  If login
// succeeds, the this function will then try invoking the authorization server's userinfo endpoint
// and persisting it using l.SaveUserInfoFunc (which would usually write to user cache).
func (l *loginExecutor) Login(ctx context.Context) (err error) {
	// We'll be making use of l.auth2config
	l.oauth2ConfigMu.RLock()
	defer l.oauth2ConfigMu.RUnlock()

	// Only one login action at a time
	l.loginMu.Lock()
	defer l.loginMu.Unlock()

	// Whatever the result is, report it to the terminal and report it to Metriton.
	var token *oauth2.Token
	defer func() {
		switch {
		case err != nil && err != ctx.Err():
			fmt.Fprintln(l.stdout, "Login failure.")
			l.scout <- scout.ScoutReport{
				Action: "login_failure",
				Metadata: map[string]interface{}{
					"error": err.Error(),
				},
			}
		case err != nil && err == ctx.Err():
			fmt.Fprintln(l.stdout, "Login aborted.")
			l.scout <- scout.ScoutReport{
				Action: "login_interrupted",
			}
		default:
			fmt.Fprintln(l.stdout, "Login successful.")
			_ = l.lockedRetrieveUserInfo(ctx, token)
			l.scout <- scout.ScoutReport{
				Action: "login_success",
			}
		}
	}()

	// create OAuth2 authentication code flow URL
	state := uuid.New().String()
	pkceVerifier, err := NewCodeVerifier()
	if err != nil {
		return err
	}
	url := l.oauth2Config.AuthCodeURL(
		state,
		oauth2.SetAuthURLParam("code_challenge", pkceVerifier.CodeChallengeS256()),
		oauth2.SetAuthURLParam("code_challenge_method", PKCEChallengeMethodS256),
	)

	fmt.Fprintln(l.stdout, "Launching browser authentication flow...")
	if err := l.OpenURLFunc(url); err != nil {
		fmt.Fprintf(l.stdout, "Could not open browser, please access this URL: %v\n", url)
	}

	// wait for callback completion or interruption
	select {
	case callback, ok := <-l.callbacks:
		if !ok {
			return errors.New("connector shutting down")
		}
		if callback.Error != "" {
			return fmt.Errorf("%s error returned on OAuth2 callback: %s", callback.Error, callback.ErrorDescription)
		}

		// retrieve access token from callback code
		token, err = l.oauth2Config.Exchange(
			ctx,
			callback.Code,
			oauth2.SetAuthURLParam("code_verifier", pkceVerifier.String()),
		)
		if err != nil {
			return fmt.Errorf("error while exchanging code for token: %w", err)
		}

		l.tokenSource = newTokenSource(ctx, l.oauth2Config, l.tokenCB, l.tokenErrCB, token)
		if err := l.tokenCB(ctx, token); err != nil {
			return err
		}

		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *loginExecutor) Logout(ctx context.Context) error {
	l.loginMu.Lock()
	defer l.loginMu.Unlock()

	if l.tokenSource == nil {
		return fmt.Errorf("Logout: %w", ErrNotLoggedIn)
	}

	l.resetRefreshTimer(0)
	l.tokenSource = nil
	_ = authdata.DeleteTokenFromUserCache(ctx)

	l.userInfo = nil
	_ = authdata.DeleteUserInfoFromUserCache(ctx)

	l.apikeys[l.env.LoginDomain] = make(map[string]string)
	if err := cache.SaveToUserCache(ctx, l.apikeys, apikeysFile); err != nil {
		return err
	}

	return nil
}

func (l *loginExecutor) GetToken(ctx context.Context) (string, error) {
	l.loginMu.Lock()
	defer l.loginMu.Unlock()

	if l.tokenSource == nil {
		return "", fmt.Errorf("GetToken: %w", ErrNotLoggedIn)
	} else if tokenInfo, err := l.tokenSource.Token(); err != nil {
		return "", err
	} else {
		return tokenInfo.AccessToken, nil
	}
}

func (l *loginExecutor) GetUserInfo(ctx context.Context) (*authdata.UserInfo, error) {
	l.loginMu.Lock()
	defer l.loginMu.Unlock()

	if l.userInfo == nil {
		return nil, fmt.Errorf("GetUserInfo: %w", ErrNotLoggedIn)
	}
	return l.userInfo, nil
}

func (l *loginExecutor) GetAPIKey(ctx context.Context, description string) (string, error) {
	l.loginMu.Lock()
	defer l.loginMu.Unlock()

	if key, ok := l.apikeys[l.env.LoginDomain][description]; ok {
		return key, nil
	} else if l.tokenSource == nil {
		return "", fmt.Errorf("GetAPIKey: %w", ErrNotLoggedIn)
	} else if tokenInfo, err := l.tokenSource.Token(); err != nil {
		return "", err
	} else if key, err := getAPIKey(ctx, l.env, tokenInfo.AccessToken, description); err != nil {
		return "", err
	} else {
		l.apikeys[l.env.LoginDomain][description] = key
		if err := cache.SaveToUserCache(ctx, l.apikeys, apikeysFile); err != nil {
			return "", err
		}
		return key, nil
	}
}

// Must hold l.loginMu to call this.
func (l *loginExecutor) lockedRetrieveUserInfo(ctx context.Context, token *oauth2.Token) error {
	var userInfo authdata.UserInfo
	req, err := http.NewRequest("GET", l.env.UserInfoURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token.AccessToken))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %v from user info endpoint", resp.StatusCode)
	}
	content, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	err = json.Unmarshal(content, &userInfo)
	if err != nil {
		return err
	}
	l.userInfo = &userInfo
	return l.SaveUserInfoFunc(ctx, &userInfo)
}

func (l *loginExecutor) httpHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != callbackPath {
		http.NotFound(w, r)
		return
	}
	query := r.URL.Query()
	code := query.Get("code")
	errorName := query.Get("error")
	errorDescription := query.Get("error_description")

	var sb strings.Builder
	sb.WriteString("<!DOCTYPE html><html><head><title>Authentication Successful</title></head><body>")
	if errorName == "" && code != "" {
		w.Header().Set("Location", l.env.LoginCompletionURL)
		w.WriteHeader(http.StatusTemporaryRedirect)
		sb.WriteString("<h1>Authentication Successful</h1>")
		sb.WriteString("<p>You can now close this tab and resume on the CLI.</p>")
	} else {
		sb.WriteString("<h1>Authentication Error</h1>")
		sb.WriteString(fmt.Sprintf("<p>%s: %s</p>", errorName, errorDescription))
		w.WriteHeader(http.StatusInternalServerError)
	}
	sb.WriteString("</body></html>")

	w.Header().Set("Content-Type", "text/html")
	if _, err := io.WriteString(w, sb.String()); err != nil {
		dlog.Errorf(r.Context(), "Error writing callback response body: %v", err)
	}

	resp := oauth2Callback{
		Code:             code,
		Error:            errorName,
		ErrorDescription: errorDescription,
	}
	// Only send the resp if there's still a listener waiting for it.  The user might have hit
	// Ctrl-C and hung up!
	select {
	case l.callbacks <- resp:
	default:
	}
}
