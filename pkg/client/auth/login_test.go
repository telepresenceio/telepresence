package auth_test

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/oauth2"
	"gotest.tools/assert"
	is "gotest.tools/assert/cmp"

	"github.com/datawire/telepresence2/pkg/client/auth"
)

type MockSaveTokenWrapper struct {
	CallArguments []*oauth2.Token
	Err           error
}

func (m *MockSaveTokenWrapper) SaveToken(token *oauth2.Token) error {
	m.CallArguments = append(m.CallArguments, token)
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
	Server                 *http.Server
	TokenRequestFormValues []url.Values
	TokenResponseCode      int
}

func newMockOauth2Server(t *testing.T) *MockOauth2Server {
	listener, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	handler := http.NewServeMux()
	server := &http.Server{
		Addr:    listener.Addr().String(),
		Handler: handler,
	}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Printf("callback server error: %v", err)
		}
	}()
	oauth2Server := &MockOauth2Server{Server: server, TokenResponseCode: http.StatusOK}
	handler.Handle("/auth", http.NotFoundHandler())
	handler.Handle("/token", oauth2Server.HandleToken())
	return oauth2Server
}

func (s *MockOauth2Server) TearDown(t *testing.T) {
	if err := s.Server.Close(); err != nil {
		t.Fatal(err)
	}
}

func (s *MockOauth2Server) AuthUrl() string {
	return s.urlForPath("/auth")
}

func (s *MockOauth2Server) TokenUrl() string {
	return s.urlForPath("/token")
}

func (s *MockOauth2Server) urlForPath(path string) string {
	return fmt.Sprintf("http://%s%s", s.Server.Addr, path)
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

func TestLoginFlow(t *testing.T) {
	type fixture struct {
		MockSaveTokenWrapper *MockSaveTokenWrapper
		MockOpenURLWrapper   *MockOpenURLWrapper
		MockOauth2Server     *MockOauth2Server
		Runner               *auth.LoginExecutor
		OpenedUrls           chan string
	}
	const mockCompletionUrl = "http://example.com/mock-completion"

	setup := func(t *testing.T) *fixture {
		mockSaveTokenWrapper := &MockSaveTokenWrapper{}
		mockOpenURLWrapper := &MockOpenURLWrapper{}
		openUrlChan := make(chan string)
		mockOauth2Server := newMockOauth2Server(t)
		return &fixture{
			MockSaveTokenWrapper: mockSaveTokenWrapper,
			MockOpenURLWrapper:   mockOpenURLWrapper,
			MockOauth2Server:     mockOauth2Server,
			OpenedUrls:           openUrlChan,
			Runner: &auth.LoginExecutor{
				Oauth2AuthUrl:  mockOauth2Server.AuthUrl(),
				Oauth2TokenUrl: mockOauth2Server.TokenUrl(),
				Oauth2ClientId: "",
				CompletionUrl:  mockCompletionUrl,
				SaveTokenFunc:  mockSaveTokenWrapper.SaveToken,
				OpenURLFunc: func(url string) error {
					openUrlChan <- url
					return mockOpenURLWrapper.OpenURL(url)
				},
			},
		}
	}
	t.Run("will save token to user cache dir if code flow is successful", func(t *testing.T) {
		// given
		t.Parallel()
		f := setup(t)
		defer f.MockOauth2Server.TearDown(t)
		errs := make(chan error)

		// when
		go func() {
			errs <- f.Runner.LoginFlow(&cobra.Command{}, []string{})
		}()
		rawAuthUrl := <-f.OpenedUrls
		callbackUrl := extractRedirectUriFromAuthUrl(t, rawAuthUrl)
		callbackQuery := callbackUrl.Query()
		callbackQuery.Set("code", "mock-code")
		callbackUrl.RawQuery = callbackQuery.Encode()
		callbackResponse := sendCallbackRequest(t, callbackUrl)
		defer callbackResponse.Body.Close()
		err := <-errs

		// then
		assert.NilError(t, err, "no error running login flow")
		assert.Assert(t, is.Equal(http.StatusTemporaryRedirect, callbackResponse.StatusCode), "callback status is 307")
		assert.Assert(t, is.Equal(mockCompletionUrl, callbackResponse.Header.Get("Location")), "location header")
		assert.Assert(t, strings.HasPrefix(rawAuthUrl, f.MockOauth2Server.AuthUrl()), "auth url")
		assert.Assert(t, is.Len(f.MockOpenURLWrapper.CallArguments, 1), "one call to open url")
		assert.Assert(t, is.Len(f.MockOauth2Server.TokenRequestFormValues, 1), "one call to the token endpoint")
		assert.Assert(t, is.Equal("mock-code", f.MockOauth2Server.TokenRequestFormValues[0].Get("code")), "code sent for exchange")
		assert.Assert(t, is.Len(f.MockSaveTokenWrapper.CallArguments, 1), "one call to save the token")
		token := f.MockSaveTokenWrapper.CallArguments[0]
		assert.Assert(t, is.Equal("mock-access-token", token.AccessToken), "access token")
		assert.Assert(t, is.Equal("mock-refresh-token", token.RefreshToken), "refresh token")
		assert.Assert(t, is.Equal("bearer", token.TokenType), "bearer token type")
		assert.Assert(t, token.Expiry.After(time.Now().Add(time.Minute*59)), "access token expires after 59 min")
		assert.Assert(t, token.Expiry.Before(time.Now().Add(time.Minute*61)), "access token expires before 61 min")
	})
	t.Run("will save token to user cache if opening up the url fails", func(t *testing.T) {
		// given
		t.Parallel()
		f := setup(t)
		defer f.MockOauth2Server.TearDown(t)
		errs := make(chan error)
		f.MockOpenURLWrapper.Err = errors.New("browser issue")

		// when
		go func() {
			errs <- f.Runner.LoginFlow(&cobra.Command{}, []string{})
		}()
		rawAuthUrl := <-f.OpenedUrls
		callbackUrl := extractRedirectUriFromAuthUrl(t, rawAuthUrl)
		callbackQuery := callbackUrl.Query()
		callbackQuery.Set("code", "mock-code")
		callbackUrl.RawQuery = callbackQuery.Encode()
		callbackResponse := sendCallbackRequest(t, callbackUrl)
		defer callbackResponse.Body.Close()
		err := <-errs

		// then
		assert.NilError(t, err, "no error running login flow")
		assert.Assert(t, is.Len(f.MockOpenURLWrapper.CallArguments, 1), "one call to open url")
		assert.Assert(t, is.Len(f.MockOauth2Server.TokenRequestFormValues, 1), "one call to the token endpoint")
		assert.Assert(t, is.Len(f.MockSaveTokenWrapper.CallArguments, 1), "one call to save the token")
	})
	t.Run("will return an error if callback is invoked with error parameters", func(t *testing.T) {
		// given
		t.Parallel()
		f := setup(t)
		defer f.MockOauth2Server.TearDown(t)
		errs := make(chan error)

		// when
		go func() {
			errs <- f.Runner.LoginFlow(&cobra.Command{}, []string{})
		}()
		rawAuthUrl := <-f.OpenedUrls
		callbackUrl := extractRedirectUriFromAuthUrl(t, rawAuthUrl)
		callbackQuery := callbackUrl.Query()
		callbackQuery.Set("code", "")
		callbackQuery.Set("error", "some_error")
		callbackQuery.Set("error_description", "some elaborate description")
		callbackUrl.RawQuery = callbackQuery.Encode()
		callbackResponse := sendCallbackRequest(t, callbackUrl)
		defer callbackResponse.Body.Close()
		err := <-errs

		// then
		assert.Assert(t, is.Error(err, "some_error error returned on OAuth2 callback: some elaborate description"), "error message")
		assert.Assert(t, is.Equal(http.StatusInternalServerError, callbackResponse.StatusCode), "callback status is 500")
		assert.Assert(t, is.Len(f.MockOpenURLWrapper.CallArguments, 1), "one call to open url")
		assert.Assert(t, is.Len(f.MockOauth2Server.TokenRequestFormValues, 0), "no call to the token endpoint")
		assert.Assert(t, is.Len(f.MockSaveTokenWrapper.CallArguments, 0), "no call to save the token")
	})
	t.Run("will return an error if the code exchange fails", func(t *testing.T) {
		// given
		t.Parallel()
		f := setup(t)
		f.MockOauth2Server.TokenResponseCode = http.StatusInternalServerError
		defer f.MockOauth2Server.TearDown(t)
		errs := make(chan error)

		// when
		go func() {
			errs <- f.Runner.LoginFlow(&cobra.Command{}, []string{})
		}()
		rawAuthUrl := <-f.OpenedUrls
		callbackUrl := extractRedirectUriFromAuthUrl(t, rawAuthUrl)
		callbackQuery := callbackUrl.Query()
		callbackQuery.Set("code", "mock-code")
		callbackUrl.RawQuery = callbackQuery.Encode()
		callbackResponse := sendCallbackRequest(t, callbackUrl)
		defer callbackResponse.Body.Close()
		err := <-errs

		// then
		assert.Assert(t, is.ErrorContains(err, "error while exchanging code for token:"), "error message")
		assert.Assert(t, is.Equal(http.StatusTemporaryRedirect, callbackResponse.StatusCode), "callback status is 307")
		assert.Assert(t, is.Equal(mockCompletionUrl, callbackResponse.Header.Get("Location")), "location header")
		assert.Assert(t, is.Len(f.MockOpenURLWrapper.CallArguments, 1), "one call to open url")
		assert.Assert(t, is.Len(f.MockOauth2Server.TokenRequestFormValues, 2), "one retry to the token endpoint")
		assert.Assert(t, is.Len(f.MockSaveTokenWrapper.CallArguments, 0), "no call to save the token")
	})
	t.Run("returns an error if the token can't be saved", func(t *testing.T) {
		// given
		t.Parallel()
		f := setup(t)
		defer f.MockOauth2Server.TearDown(t)
		errs := make(chan error)
		f.MockSaveTokenWrapper.Err = errors.New("disk error")

		// when
		go func() {
			errs <- f.Runner.LoginFlow(&cobra.Command{}, []string{})
		}()
		rawAuthUrl := <-f.OpenedUrls
		callbackUrl := extractRedirectUriFromAuthUrl(t, rawAuthUrl)
		callbackQuery := callbackUrl.Query()
		callbackQuery.Set("code", "mock-code")
		callbackUrl.RawQuery = callbackQuery.Encode()
		callbackResponse := sendCallbackRequest(t, callbackUrl)
		defer callbackResponse.Body.Close()
		err := <-errs

		// then
		assert.Assert(t, is.Error(err, "could not save access token to user cache: disk error"), "error message")
		assert.Assert(t, is.Equal(http.StatusTemporaryRedirect, callbackResponse.StatusCode), "callback status is 307")
		assert.Assert(t, is.Equal(mockCompletionUrl, callbackResponse.Header.Get("Location")), "location header")
		assert.Assert(t, is.Len(f.MockOpenURLWrapper.CallArguments, 1), "one call to open url")
		assert.Assert(t, is.Len(f.MockOauth2Server.TokenRequestFormValues, 1), "one retry to the token endpoint")
		assert.Assert(t, is.Len(f.MockSaveTokenWrapper.CallArguments, 1), "no call to save the token")
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
