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
	"os/signal"
	"strings"
	"syscall"

	"github.com/google/uuid"
	"github.com/pkg/browser"
	"golang.org/x/oauth2"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cache"
)

const (
	callbackPath = "/callback"
)

type oauth2Callback struct {
	Code             string
	Error            string
	ErrorDescription string
}

type loginExecutor struct {
	env              client.Env
	SaveTokenFunc    func(context.Context, *oauth2.Token) error
	SaveUserInfoFunc func(context.Context, *cache.UserInfo) error
	OpenURLFunc      func(string) error
	Scout            *client.Scout
}

// LoginExecutor controls the execution of a login flow
type LoginExecutor interface {
	LoginFlow(ctx context.Context, stdout, stderr io.Writer) error
}

// NewLoginExecutor returns an instance of LoginExecutor
func NewLoginExecutor(
	env client.Env,
	saveTokenFunc func(context.Context, *oauth2.Token) error,
	saveUserInfoFunc func(context.Context, *cache.UserInfo) error,
	openURLFunc func(string) error,
	scout *client.Scout) LoginExecutor {
	return &loginExecutor{
		env:              env,
		SaveTokenFunc:    saveTokenFunc,
		SaveUserInfoFunc: saveUserInfoFunc,
		OpenURLFunc:      openURLFunc,
		Scout:            scout,
	}
}

// EnsureLoggedIn will check if the user is logged in and if not initiate the login flow.
func EnsureLoggedIn(ctx context.Context, stdout, stderr io.Writer) error {
	if token, _ := cache.LoadTokenFromUserCache(ctx); token != nil {
		return nil
	}

	return Login(ctx, stdout, stderr)
}

func Login(ctx context.Context, stdout, stderr io.Writer) error {
	env, err := client.LoadEnv(ctx)
	if err != nil {
		return err
	}

	l := NewLoginExecutor(
		env,
		cache.SaveTokenToUserCache,
		cache.SaveUserInfoToUserCache,
		browser.OpenURL,
		client.NewScout(ctx, "cli"),
	)
	return l.LoginFlow(ctx, stdout, stderr)
}

// LoginFlow tries logging the user by opening a browser window and authenticating against the
// configured OAuth2 provider user a code flow. An HTTP server is started in the background during
// authentication to handle the callback url. Once the callback url is invoked, the login flow will
// invoke the token endpoint to get the user's access & refresh tokens and persist them with the
// SaveTokenFunc (which would usually write to user cache).
// If login succeeds, the login flow will then try invoking the userinfo endpoint and persisting it
// using SaveUserInfoFunc (which would usually write to user cache).
func (l *loginExecutor) LoginFlow(ctx context.Context, stdout, stderr io.Writer) error {
	// oauth2Callback chan that will receive the callback info
	callbacks := make(chan oauth2Callback)
	// also listen for interruption to cancel the flow
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, syscall.SIGINT, syscall.SIGTERM)

	// start the background server on which we'll be listening for the OAuth2 callback
	backgroundServer, err := startBackgroundServer(callbacks, stderr, l.env.LoginCompletionURL)
	defer func() {
		err := backgroundServer.Shutdown(context.Background())
		if err != nil {
			fmt.Fprintf(stderr, "error shutting down callback server: %v", err)
		}
	}()
	if err != nil {
		return err
	}
	oauth2Config := oauth2.Config{
		ClientID:    l.env.LoginClientID,
		RedirectURL: fmt.Sprintf("http://%v%v", backgroundServer.Addr, callbackPath),
		Endpoint: oauth2.Endpoint{
			AuthURL:  l.env.LoginAuthURL,
			TokenURL: l.env.LoginTokenURL,
		},
		Scopes: []string{"openid", "profile", "email"},
	}

	// create OAuth2 authentication code flow URL
	state := uuid.New().String()
	pkceVerifier, err := NewCodeVerifier()
	if err != nil {
		return err
	}
	url := oauth2Config.AuthCodeURL(
		state,
		oauth2.SetAuthURLParam("code_challenge", pkceVerifier.CodeChallengeS256()),
		oauth2.SetAuthURLParam("code_challenge_method", PKCEChallengeMethodS256),
	)

	fmt.Fprintln(stdout, "Launching browser authentication flow...")
	if err := l.OpenURLFunc(url); err != nil {
		fmt.Fprintf(stdout, "Could not open browser, please access this URL: %v\n", url)
	}

	// wait for callback completion or interruption
	select {
	case callback := <-callbacks:
		token, err := l.handleCallback(ctx, callback, oauth2Config, pkceVerifier)
		if err != nil {
			_ = l.Scout.Report(ctx, "login_failure", client.ScoutMeta{Key: "error", Value: err.Error()})
		} else {
			fmt.Fprintln(stdout, "Login successful.")
			_ = l.retrieveUserInfo(ctx, token)
			_ = l.Scout.Report(ctx, "login_success")
		}
		return err
	case <-interrupts:
		fmt.Fprintln(stdout, "Login aborted.")
		_ = l.Scout.Report(ctx, "login_interrupted")
		return nil
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

	err = l.SaveTokenFunc(ctx, token)
	if err != nil {
		return nil, fmt.Errorf("could not save access token to user cache: %w", err)
	}

	return token, nil
}

func (l *loginExecutor) retrieveUserInfo(ctx context.Context, token *oauth2.Token) error {
	var userInfo cache.UserInfo
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
	return l.SaveUserInfoFunc(ctx, &userInfo)
}

func startBackgroundServer(callbacks chan oauth2Callback, stderr io.Writer, completionUrl string) (*http.Server, error) {
	// start listening on the next available port
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		return &http.Server{}, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	handler := http.NewServeMux()
	handler.HandleFunc(callbackPath, newCallbackHandlerFunc(callbacks, stderr, completionUrl))
	server := &http.Server{
		Addr:    fmt.Sprintf("localhost:%v", port),
		Handler: handler,
	}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			callbacks <- oauth2Callback{
				Code:             "",
				Error:            "Could not start callback server",
				ErrorDescription: err.Error(),
			}
		}
	}()
	return server, nil
}

func newCallbackHandlerFunc(callbacks chan oauth2Callback, stderr io.Writer, completionUrl string) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		code := query.Get("code")
		errorName := query.Get("error")
		errorDescription := query.Get("error_description")

		var sb strings.Builder
		sb.WriteString("<!DOCTYPE html><html><head><title>Authentication Successful</title></head><body>")
		if errorName == "" && code != "" {
			w.Header().Set("Location", completionUrl)
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
			fmt.Fprintf(stderr, "Error writing callback response body: %v", err)
		}

		callbacks <- oauth2Callback{
			Code:             code,
			Error:            errorName,
			ErrorDescription: errorDescription,
		}
	}
}
