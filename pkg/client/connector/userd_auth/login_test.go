package userd_auth_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"

	"github.com/datawire/dlib/dcontext"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dhttp"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/connector/userd_auth"
	"github.com/telepresenceio/telepresence/v2/pkg/client/connector/userd_auth/authdata"
	"github.com/telepresenceio/telepresence/v2/pkg/client/scout"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

type MockSaveTokenWrapper struct {
	CallArguments []*oauth2.Token
	Err           error
}

func (m *MockSaveTokenWrapper) SaveToken(_ context.Context, token *oauth2.Token) error {
	m.CallArguments = append(m.CallArguments, token)
	return m.Err
}

type MockSaveUserInfoWrapper struct {
	CallArguments []*authdata.UserInfo
	Err           error
}

func (m *MockSaveUserInfoWrapper) SaveUserInfo(_ context.Context, userInfo *authdata.UserInfo) error {
	m.CallArguments = append(m.CallArguments, userInfo)
	return m.Err
}

type MockOpenURLWrapper struct {
	CallArguments []string
	Err           error
}

func (m *MockOpenURLWrapper) OpenURL(url string) error {
	m.CallArguments = append(m.CallArguments, url)
	return m.Err
}

type MockOauth2Server struct {
	ServerConfig  *dhttp.ServerConfig
	ServerAddress string
	ServerStop    func() error

	TokenRequestFormValues []url.Values
	TokenResponseCode      int
	UserInfo               *authdata.UserInfo
}

func newMockOauth2Server(t *testing.T) *MockOauth2Server {
	listener, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	handler := http.NewServeMux()
	sc := &dhttp.ServerConfig{
		Handler: handler,
	}
	ctx, cancel := context.WithCancel(dlog.NewTestContext(t, true))
	ch := make(chan error)
	go func() {
		ch <- sc.Serve(ctx, listener)
		close(ch)
	}()
	oauth2Server := &MockOauth2Server{
		ServerConfig:  sc,
		ServerAddress: listener.Addr().String(),
		ServerStop: func() error {
			cancel()
			return <-ch
		},
		TokenResponseCode: http.StatusOK,
	}
	oauth2Server.UserInfo = &authdata.UserInfo{
		Id:               "mock-user-id",
		Name:             "mock-user-name",
		AvatarUrl:        "mock-user-avatar-url",
		AccountId:        "mock-account-id",
		AccountName:      "mock-account-name",
		AccountAvatarUrl: "mock-account-avatar-url",
	}
	handler.Handle("/auth", http.NotFoundHandler())
	handler.Handle("/token", oauth2Server.HandleToken())
	handler.Handle("/api/userinfo", oauth2Server.HandleUserInfo())
	return oauth2Server
}

func (s *MockOauth2Server) TearDown(t *testing.T) {
	if err := s.ServerStop(); err != nil {
		t.Logf("mock oauth2 server: %v", err)
	}
}

func (s *MockOauth2Server) AuthUrl() string {
	return s.urlForPath("/auth")
}

func (s *MockOauth2Server) TokenUrl() string {
	return s.urlForPath("/token")
}

func (s *MockOauth2Server) UserInfoUrl() string {
	return s.urlForPath("/api/userinfo")
}

func (s *MockOauth2Server) urlForPath(path string) string {
	return fmt.Sprintf("http://%s%s", s.ServerAddress, path)
}

func (s *MockOauth2Server) HandleToken() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		s.TokenRequestFormValues = append(s.TokenRequestFormValues, r.Form)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(s.TokenResponseCode)
		_, _ = w.Write([]byte(`{
				"access_token": "mock-access-token",
				"expires_in": 3600,
				"refresh_token": "mock-refresh-token",
				"token_type": "bearer",
				"not-before-policy": 0
			}`,
		))
	})
}

func (s *MockOauth2Server) HandleUserInfo() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.UserInfo == nil {
			http.NotFound(w, r)
			return
		}
		bytes, err := json.Marshal(s.UserInfo)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write(bytes)
	})
}

func TestLoginFlow(t *testing.T) {
	type fixture struct {
		Context                 context.Context
		MockSaveTokenWrapper    *MockSaveTokenWrapper
		MockSaveUserInfoWrapper *MockSaveUserInfoWrapper
		MockOpenURLWrapper      *MockOpenURLWrapper
		MockOauth2Server        *MockOauth2Server
		Runner                  userd_auth.LoginExecutor
		OpenedUrls              chan string
	}
	const mockCompletionUrl = "http://example.com/mock-completion"

	setupWithCacheFuncs := func(
		t *testing.T,
		saveTokenFunc func(context.Context, *oauth2.Token) error,
		saveUserInfoFunc func(context.Context, *authdata.UserInfo) error,
	) *fixture {
		mockSaveTokenWrapper := &MockSaveTokenWrapper{}
		saveToken := saveTokenFunc
		if saveToken == nil {
			saveToken = mockSaveTokenWrapper.SaveToken
		}
		mockSaveUserInfoWrapper := &MockSaveUserInfoWrapper{}
		saveUserInfo := saveUserInfoFunc
		if saveUserInfo == nil {
			saveUserInfo = mockSaveUserInfoWrapper.SaveUserInfo
		}
		mockOpenURLWrapper := &MockOpenURLWrapper{}
		openUrlChan := make(chan string)
		mockOauth2Server := newMockOauth2Server(t)
		ctx := dlog.NewTestContext(t, false)
		stdout := dlog.StdLogger(ctx, dlog.LogLevelInfo).Writer()
		scout := make(chan scout.ScoutReport)
		t.Cleanup(func() { close(scout) })
		go func() {
			for range scout {
			}
		}()
		return &fixture{
			Context: client.WithEnv(ctx,
				&client.Env{
					LoginAuthURL:       mockOauth2Server.AuthUrl(),
					LoginTokenURL:      mockOauth2Server.TokenUrl(),
					LoginClientID:      "",
					LoginCompletionURL: mockCompletionUrl,
					UserInfoURL:        mockOauth2Server.UserInfoUrl(),
				}),
			MockSaveTokenWrapper:    mockSaveTokenWrapper,
			MockSaveUserInfoWrapper: mockSaveUserInfoWrapper,
			MockOpenURLWrapper:      mockOpenURLWrapper,
			MockOauth2Server:        mockOauth2Server,
			OpenedUrls:              openUrlChan,
			Runner: userd_auth.NewLoginExecutor(
				saveToken,
				saveUserInfo,
				func(url string) error {
					openUrlChan <- url
					return mockOpenURLWrapper.OpenURL(url)
				},
				stdout,
				scout,
			),
		}
	}
	setup := func(t *testing.T) *fixture {
		return setupWithCacheFuncs(t, nil, nil)
	}
	executeLoginFlowWithErrorParam := func(t *testing.T, f *fixture, errorCode, errorDescription string) (*http.Response, string, error) {
		grp := dgroup.NewGroup(f.Context, dgroup.GroupConfig{
			EnableWithSoftness: true,
			ShutdownOnNonError: true,
		})
		grp.Go("worker", f.Runner.Worker)
		grp.Go("login", f.Runner.Login)
		rawAuthUrl := <-f.OpenedUrls
		callbackUrl := extractRedirectUriFromAuthUrl(t, rawAuthUrl)
		callbackQuery := callbackUrl.Query()
		if errorCode == "" {
			callbackQuery.Set("code", "mock-code")
		} else {
			callbackQuery.Set("error", errorCode)
			callbackQuery.Set("error_description", errorDescription)
		}
		callbackUrl.RawQuery = callbackQuery.Encode()
		callbackResponse := sendCallbackRequest(t, callbackUrl)
		return callbackResponse, rawAuthUrl, grp.Wait()
	}
	executeDefaultLoginFlow := func(t *testing.T, f *fixture) (*http.Response, string, error) {
		return executeLoginFlowWithErrorParam(t, f, "", "")
	}
	t.Run("will save token to user cache dir if code flow is successful", func(t *testing.T) {
		// given
		t.Parallel()
		f := setup(t)
		defer f.MockOauth2Server.TearDown(t)

		// when
		callbackResponse, rawAuthUrl, err := executeDefaultLoginFlow(t, f)

		// then
		assert.NoError(t, err, "no error running login flow")
		defer callbackResponse.Body.Close()
		assert.Equal(t, http.StatusTemporaryRedirect, callbackResponse.StatusCode, "callback status is 307")
		assert.Equal(t, mockCompletionUrl, callbackResponse.Header.Get("Location"), "location header")
		assert.True(t, strings.HasPrefix(rawAuthUrl, f.MockOauth2Server.AuthUrl()), "auth url")
		assert.Len(t, f.MockOpenURLWrapper.CallArguments, 1, "one call to open url")
		assert.Len(t, f.MockOauth2Server.TokenRequestFormValues, 1, "one call to the token endpoint")
		assert.Equal(t, "mock-code", f.MockOauth2Server.TokenRequestFormValues[0].Get("code"), "code sent for exchange")
		assert.Len(t, f.MockSaveTokenWrapper.CallArguments, 1, "one call to save the token")
		token := f.MockSaveTokenWrapper.CallArguments[0]
		assert.Equal(t, "mock-access-token", token.AccessToken, "access token")
		assert.Equal(t, "mock-refresh-token", token.RefreshToken, "refresh token")
		assert.Len(t, f.MockSaveUserInfoWrapper.CallArguments, 1, "one call to save the user info")
		assert.Equal(t, "bearer", token.TokenType, "bearer token type")
		assert.True(t, token.Expiry.After(time.Now().Add(time.Minute*59)), "access token expires after 59 min")
		assert.True(t, token.Expiry.Before(time.Now().Add(time.Minute*61)), "access token expires before 61 min")
		userInfo := f.MockSaveUserInfoWrapper.CallArguments[0]
		assert.Equal(t, "mock-user-id", userInfo.Id, "user id")
		assert.Equal(t, "mock-user-name", userInfo.Name, "user name")
		assert.Equal(t, "mock-user-avatar-url", userInfo.AvatarUrl, "user avatar url")
		assert.Equal(t, "mock-account-id", userInfo.AccountId, "account id")
		assert.Equal(t, "mock-account-name", userInfo.AccountName, "account name")
		assert.Equal(t, "mock-account-avatar-url", userInfo.AccountAvatarUrl, "account avatar url")
	})
	t.Run("will save token to user cache if opening up the url fails", func(t *testing.T) {
		// given
		t.Parallel()
		f := setup(t)
		defer f.MockOauth2Server.TearDown(t)
		f.MockOpenURLWrapper.Err = errors.New("browser issue")

		// when
		callbackResponse, _, err := executeDefaultLoginFlow(t, f)

		// then
		assert.NoError(t, err, "no error running login flow")
		defer callbackResponse.Body.Close()
		assert.Len(t, f.MockOpenURLWrapper.CallArguments, 1, "one call to open url")
		assert.Len(t, f.MockOauth2Server.TokenRequestFormValues, 1, "one call to the token endpoint")
		assert.Len(t, f.MockSaveTokenWrapper.CallArguments, 1, "one call to save the token")
		assert.Len(t, f.MockSaveUserInfoWrapper.CallArguments, 1, "one call to save the user info")
	})
	t.Run("will return error if user info retrieval fails", func(t *testing.T) {
		// given
		t.Parallel()
		f := setup(t)
		defer f.MockOauth2Server.TearDown(t)
		f.MockOauth2Server.UserInfo = nil

		// when
		callbackResponse, _, err := executeDefaultLoginFlow(t, f)

		// then
		assert.EqualError(t, err, "unexpected status 404 from user info endpoint")
		defer callbackResponse.Body.Close()
		assert.Len(t, f.MockOpenURLWrapper.CallArguments, 1, "one call to open url")
		assert.Len(t, f.MockOauth2Server.TokenRequestFormValues, 1, "one call to the token endpoint")
		assert.Len(t, f.MockSaveTokenWrapper.CallArguments, 1, "one call to save the token")
		assert.Len(t, f.MockSaveUserInfoWrapper.CallArguments, 0, "no call to save the user info")
	})
	t.Run("will return error if user info persistence fails", func(t *testing.T) {
		// given
		t.Parallel()
		f := setup(t)
		defer f.MockOauth2Server.TearDown(t)
		f.MockSaveUserInfoWrapper.Err = errors.New("could not save user info")

		// when
		callbackResponse, _, err := executeDefaultLoginFlow(t, f)

		// then
		assert.EqualError(t, err, "could not save user info")
		defer callbackResponse.Body.Close()
		assert.Len(t, f.MockOpenURLWrapper.CallArguments, 1, "one call to open url")
		assert.Len(t, f.MockOauth2Server.TokenRequestFormValues, 1, "one call to the token endpoint")
		assert.Len(t, f.MockSaveTokenWrapper.CallArguments, 1, "one call to save the token")
		assert.Len(t, f.MockSaveUserInfoWrapper.CallArguments, 1, "one call to save the user info")
	})
	t.Run("will return an error if callback is invoked with error parameters", func(t *testing.T) {
		// given
		t.Parallel()
		f := setup(t)
		defer f.MockOauth2Server.TearDown(t)

		// when
		callbackResponse, _, err := executeLoginFlowWithErrorParam(t, f, "some_error", "some elaborate description")

		// then
		assert.EqualError(t, err, "some_error error returned on OAuth2 callback: some elaborate description", "error message")
		defer callbackResponse.Body.Close()
		assert.Equal(t, http.StatusInternalServerError, callbackResponse.StatusCode, "callback status is 500")
		assert.Len(t, f.MockOpenURLWrapper.CallArguments, 1, "one call to open url")
		assert.Len(t, f.MockOauth2Server.TokenRequestFormValues, 0, "no call to the token endpoint")
		assert.Len(t, f.MockSaveTokenWrapper.CallArguments, 0, "no call to save the token")
		assert.Len(t, f.MockSaveUserInfoWrapper.CallArguments, 0, "no call to save the user info")
	})
	t.Run("will return an error if the code exchange fails", func(t *testing.T) {
		// given
		t.Parallel()
		f := setup(t)
		f.MockOauth2Server.TokenResponseCode = http.StatusInternalServerError
		defer f.MockOauth2Server.TearDown(t)

		// when
		callbackResponse, _, err := executeDefaultLoginFlow(t, f)

		// then
		if assert.Error(t, err) {
			assert.Contains(t, err.Error(), "error while exchanging code for token:", "error message")
		}
		defer callbackResponse.Body.Close()
		assert.Equal(t, http.StatusTemporaryRedirect, callbackResponse.StatusCode, "callback status is 307")
		assert.Equal(t, mockCompletionUrl, callbackResponse.Header.Get("Location"), "location header")
		assert.Len(t, f.MockOpenURLWrapper.CallArguments, 1, "one call to open url")
		assert.Len(t, f.MockOauth2Server.TokenRequestFormValues, 2, "one retry to the token endpoint")
		assert.Len(t, f.MockSaveTokenWrapper.CallArguments, 0, "no call to save the token")
		assert.Len(t, f.MockSaveUserInfoWrapper.CallArguments, 0, "no call to save the user info")
	})
	t.Run("returns an error if the token can't be saved", func(t *testing.T) {
		// given
		t.Parallel()
		f := setup(t)
		defer f.MockOauth2Server.TearDown(t)
		f.MockSaveTokenWrapper.Err = errors.New("disk error")

		// when
		callbackResponse, _, err := executeDefaultLoginFlow(t, f)

		// then
		assert.EqualError(t, err, "could not save access token to user cache: disk error", "error message")
		defer callbackResponse.Body.Close()
		assert.Equal(t, http.StatusTemporaryRedirect, callbackResponse.StatusCode, "callback status is 307")
		assert.Equal(t, mockCompletionUrl, callbackResponse.Header.Get("Location"), "location header")
		assert.Len(t, f.MockOpenURLWrapper.CallArguments, 1, "one call to open url")
		assert.Len(t, f.MockOauth2Server.TokenRequestFormValues, 1, "one retry to the token endpoint")
		assert.Len(t, f.MockSaveTokenWrapper.CallArguments, 1, "one call to save the token")
		assert.Len(t, f.MockSaveUserInfoWrapper.CallArguments, 0, "no call to save the user info")
	})

	t.Run("will remove token and user info from user cache dir when logging out", func(t *testing.T) {
		// given
		f := setupWithCacheFuncs(t, authdata.SaveTokenToUserCache, authdata.SaveUserInfoToUserCache)
		defer f.MockOauth2Server.TearDown(t)

		// a fake user cache directory
		ctx := filelocation.WithUserHomeDir(f.Context, t.TempDir())

		// when
		ctx, cancel := context.WithCancel(dcontext.WithSoftness(ctx))
		grp := dgroup.NewGroup(ctx, dgroup.GroupConfig{})
		grp.Go("worker", f.Runner.Worker)
		loginErrCh := make(chan error)
		grp.Go("login", func(ctx context.Context) error {
			err := f.Runner.Login(ctx)
			loginErrCh <- err
			return err
		})
		rawAuthUrl := <-f.OpenedUrls
		callbackUrl := extractRedirectUriFromAuthUrl(t, rawAuthUrl)
		callbackQuery := callbackUrl.Query()
		callbackQuery.Set("code", "mock-code")
		callbackUrl.RawQuery = callbackQuery.Encode()
		callbackResponse := sendCallbackRequest(t, callbackUrl)
		defer callbackResponse.Body.Close()
		err := <-loginErrCh

		// then
		t.Log("then")
		require.NoError(t, err, "no error running login flow")
		token, err := authdata.LoadTokenFromUserCache(ctx)
		require.NoError(t, err, "no error reading token")
		require.NotNil(t, token)
		userInfo, err := authdata.LoadUserInfoFromUserCache(ctx)
		require.NoError(t, err, "no error reading user info")
		require.NotNil(t, userInfo)
		err = f.Runner.Logout(ctx)
		require.NoError(t, err, "no error executing logout")
		_, err = authdata.LoadTokenFromUserCache(ctx)
		require.Error(t, err, "error reading token")
		_, err = authdata.LoadUserInfoFromUserCache(ctx)
		require.Error(t, err, "error reading user info")
		err = f.Runner.Logout(ctx)
		require.Error(t, err, "error executing logout when not logged in")
		t.Log("cleaning up")
		cancel()
		require.NoError(t, grp.Wait())
	})
}

func sendCallbackRequest(t *testing.T, callbackUrl *url.URL) *http.Response {
	// don't follow redirects
	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	callbackResponse, err := client.Get(callbackUrl.String())

	if err != nil {
		t.Fatal(err)
	}
	return callbackResponse
}

func extractRedirectUriFromAuthUrl(t *testing.T, rawAuthUrl string) *url.URL {
	openedUrl, err := url.Parse(rawAuthUrl)
	if err != nil {
		t.Fatal(err)
	}
	callbackUrl, err := url.Parse(openedUrl.Query().Get("redirect_uri"))
	if err != nil {
		t.Fatal(err)
	}
	return callbackUrl
}
