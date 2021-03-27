package auth

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
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/connector/auth/authdata"
	"github.com/telepresenceio/telepresence/v2/pkg/client/connector/internal/scout"
)

const (
	callbackPath = "/callback"
)

var ErrNotLoggedIn = errors.New("not logged in")

type oauth2Callback struct {
	Code             string
	Error            string
	ErrorDescription string
}

type tokenResp struct {
	token string
	err   error
}

type userInfoResp struct {
	userInfo *authdata.UserInfo
	err      error
}

type loginExecutor struct {
	// static
	env              client.Env
	SaveTokenFunc    func(context.Context, *oauth2.Token) error
	SaveUserInfoFunc func(context.Context, *authdata.UserInfo) error
	OpenURLFunc      func(string) error

	// stateful

	stdout    io.Writer
	scout     chan<- scout.ScoutReport
	callbacks chan oauth2Callback

	loginMu      sync.Mutex
	loginReq     chan context.Context
	loginResp    chan error
	logoutReq    chan struct{}
	logoutResp   chan error
	tokenReq     chan struct{}
	tokenResp    chan tokenResp
	userInfoReq  chan struct{}
	userInfoResp chan userInfoResp
}

// LoginExecutor controls the execution of a login flow
type LoginExecutor interface {
	Worker(ctx context.Context) error
	Login(ctx context.Context) error
	Logout(ctx context.Context) error
	GetToken(ctx context.Context) (string, error)
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
	return &loginExecutor{
		env:              env,
		SaveTokenFunc:    saveTokenFunc,
		SaveUserInfoFunc: saveUserInfoFunc,
		OpenURLFunc:      openURLFunc,

		stdout:    stdout,
		scout:     scout,
		callbacks: make(chan oauth2Callback),

		loginReq:     make(chan context.Context),
		loginResp:    make(chan error),
		logoutReq:    make(chan struct{}),
		logoutResp:   make(chan error),
		tokenReq:     make(chan struct{}),
		tokenResp:    make(chan tokenResp),
		userInfoReq:  make(chan struct{}),
		userInfoResp: make(chan userInfoResp),
	}
}

// EnsureLoggedIn will check if the user is logged in and if not initiate the login flow.
func EnsureLoggedIn(ctx context.Context, executor LoginExecutor) (connector.LoginResult_Code, error) {
	if token, err := executor.GetToken(ctx); err == nil && token != "" {
		return connector.LoginResult_OLD_LOGIN_REUSED, nil
	}

	if err := executor.Login(ctx); err != nil {
		return connector.LoginResult_UNSPECIFIED, err
	}

	return connector.LoginResult_NEW_LOGIN_SUCCEEDED, nil
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

// nolint:gocognit,gocyclo
func (l *loginExecutor) Worker(ctx context.Context) error {
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		return err
	}
	oauth2Config := oauth2.Config{
		ClientID:    l.env.LoginClientID,
		RedirectURL: fmt.Sprintf("http://localhost:%d%s", listener.Addr().(*net.TCPAddr).Port, callbackPath),
		Endpoint: oauth2.Endpoint{
			AuthURL:  l.env.LoginAuthURL,
			TokenURL: l.env.LoginTokenURL,
		},
		Scopes: []string{"openid", "profile", "email"},
	}

	grp := dgroup.NewGroup(ctx, dgroup.GroupConfig{
		EnableWithSoftness: ctx == dcontext.HardContext(ctx),
		ShutdownOnNonError: true,
	})

	grp.Go("server-http", func(ctx context.Context) error {
		defer close(l.callbacks)

		sc := dhttp.ServerConfig{
			Handler: http.HandlerFunc(l.httpHandler),
		}
		return sc.Serve(ctx, listener)
	})
	grp.Go("actor", func(ctx context.Context) error {
		timer := time.NewTimer(1 * time.Minute)
		timerIsStopped := false
		resetTimer := func(delta time.Duration) {
			if !timerIsStopped {
				if !timer.Stop() {
					<-timer.C
				}
				timerIsStopped = true
			}
			if delta > 0 {
				timer.Reset(delta)
			}
		}
		resetTimer(0)

		loginCtx := context.Background()
		var pkceVerifier CodeVerifier
		tokenCB := func(ctx context.Context, tokenInfo *oauth2.Token) error {
			if err := l.SaveTokenFunc(ctx, tokenInfo); err != nil {
				return fmt.Errorf("could not save access token to user cache: %w", err)
			}
			resetTimer(time.Until(tokenInfo.Expiry))
			return nil
		}
		tokenSource, err := func() (oauth2.TokenSource, error) {
			tokenInfo, err := authdata.LoadTokenFromUserCache(ctx)
			if err != nil || tokenInfo == nil {
				return nil, err
			}
			resetTimer(time.Until(tokenInfo.Expiry))
			return newTokenSource(ctx, oauth2Config, tokenInfo, false, tokenCB)
		}()
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		userInfo, err := authdata.LoadUserInfoFromUserCache(ctx)
		if err != nil && !os.IsNotExist(err) {
			return err
		}

		for {
			select {
			case loginCtx = <-l.loginReq:
				var err error
				pkceVerifier, err = NewCodeVerifier()
				if err != nil {
					return err
				}
				if err := l.startLogin(ctx, oauth2Config, pkceVerifier); err != nil {
					loginCtx = context.Background()
					maybeSend(l.loginResp, err)
				}
			case callback := <-l.callbacks:
				loginCtx = context.Background()
				tokenInfo, err := l.handleCallback(ctx, callback, oauth2Config, pkceVerifier)
				if err == nil {
					tokenSource, err = newTokenSource(ctx, oauth2Config, tokenInfo, true, tokenCB)
				}
				if err != nil {
					l.scout <- scout.ScoutReport{
						Action: "login_failure",
						Metadata: map[string]interface{}{
							"error": err.Error(),
						},
					}
				} else {
					fmt.Fprintln(l.stdout, "Login successful.")
					l.scout <- scout.ScoutReport{
						Action: "login_success",
					}

					userInfo, _ = l.retrieveUserInfo(ctx, tokenInfo)
					if userInfo != nil {
						_ = l.SaveUserInfoFunc(ctx, userInfo)
					}
				}
				maybeSend(l.loginResp, err)
			case <-loginCtx.Done():
				maybeSend(l.loginResp, loginCtx.Err())
				loginCtx = context.Background()
				fmt.Fprintln(l.stdout, "Login aborted.")
				l.scout <- scout.ScoutReport{
					Action: "login_interrupted",
				}
			case <-l.logoutReq:
				if tokenSource == nil {
					maybeSend(l.logoutResp, ErrNotLoggedIn)
				} else {
					resetTimer(0)
					tokenSource = nil
					userInfo = nil
					_ = authdata.DeleteTokenFromUserCache(ctx)
					_ = authdata.DeleteUserInfoFromUserCache(ctx)
					maybeSend(l.logoutResp, nil)
				}
			case <-l.tokenReq:
				var resp tokenResp
				if tokenSource == nil {
					resp.err = ErrNotLoggedIn
				} else if tokenInfo, err := tokenSource.Token(); err != nil {
					resp.err = err
				} else {
					resp.token = tokenInfo.AccessToken
				}
				select {
				case l.tokenResp <- resp:
				default:
				}
			case <-l.userInfoReq:
				var resp userInfoResp
				if userInfo != nil {
					resp.userInfo = userInfo
				} else {
					resp.err = ErrNotLoggedIn
				}
				select {
				case l.userInfoResp <- resp:
				default:
				}
			case <-timer.C:
				dlog.Infoln(ctx, "refreshing access token...")
				tokenInfo, err := tokenSource.Token()
				if err != nil {
					dlog.Infof(ctx, "could not refresh assess token: %v", err)
				} else if tokenInfo != nil {
					dlog.Infof(ctx, "got new access token: %v", err)
				}
			case <-ctx.Done():
				resetTimer(0)
				return nil
			}
		}
	})

	return grp.Wait()
}

func (l *loginExecutor) startLogin(ctx context.Context, oauth2Config oauth2.Config, pkceVerifier CodeVerifier) error {
	// create OAuth2 authentication code flow URL
	state := uuid.New().String()
	url := oauth2Config.AuthCodeURL(
		state,
		oauth2.SetAuthURLParam("code_challenge", pkceVerifier.CodeChallengeS256()),
		oauth2.SetAuthURLParam("code_challenge_method", PKCEChallengeMethodS256),
	)

	fmt.Fprintln(l.stdout, "Launching browser authentication flow...")
	if err := l.OpenURLFunc(url); err != nil {
		fmt.Fprintf(l.stdout, "Could not open browser, please access this URL: %v\n", url)
	}

	return nil
}

func (l *loginExecutor) Login(ctx context.Context) error {
	l.loginMu.Lock()
	defer l.loginMu.Unlock()
	l.loginReq <- ctx
	return <-l.loginResp
}

func (l *loginExecutor) Logout(ctx context.Context) error {
	l.loginMu.Lock()
	defer l.loginMu.Unlock()
	l.logoutReq <- struct{}{}
	var err error
	select {
	case err = <-l.logoutResp:
	case <-ctx.Done():
		err = ctx.Err()
	}
	return err
}

func (l *loginExecutor) GetToken(ctx context.Context) (string, error) {
	l.loginMu.Lock()
	defer l.loginMu.Unlock()
	l.tokenReq <- struct{}{}
	select {
	case resp := <-l.tokenResp:
		return resp.token, resp.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (l *loginExecutor) GetUserInfo(ctx context.Context) (*authdata.UserInfo, error) {
	l.loginMu.Lock()
	defer l.loginMu.Unlock()
	l.userInfoReq <- struct{}{}
	select {
	case resp := <-l.userInfoResp:
		return resp.userInfo, resp.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func maybeSend(ch chan<- error, err error) {
	select {
	case ch <- err:
	default:
	}
}

func (l *loginExecutor) handleCallback(
	ctx context.Context,
	callback oauth2Callback, oauth2Config oauth2.Config, pkceVerifier CodeVerifier,
) (*oauth2.Token, error) {
	if callback.Error != "" {
		return nil, fmt.Errorf("%v error returned on OAuth2 callback: %v", callback.Error, callback.ErrorDescription)
	}

	// retrieve access token from callback code
	token, err := oauth2Config.Exchange(
		ctx,
		callback.Code,
		oauth2.SetAuthURLParam("code_verifier", pkceVerifier.String()),
	)
	if err != nil {
		return nil, fmt.Errorf("error while exchanging code for token: %w", err)
	}

	return token, nil
}

func (l *loginExecutor) retrieveUserInfo(ctx context.Context, token *oauth2.Token) (*authdata.UserInfo, error) {
	var userInfo authdata.UserInfo
	req, err := http.NewRequest("GET", l.env.UserInfoURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token.AccessToken))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %v from user info endpoint", resp.StatusCode)
	}
	content, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal(content, &userInfo)
	if err != nil {
		return nil, err
	}
	return &userInfo, nil
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

	l.callbacks <- oauth2Callback{
		Code:             code,
		Error:            errorName,
		ErrorDescription: errorDescription,
	}
}
