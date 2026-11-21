package clientcreds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

type tokenRequest struct {
	resource   string
	headerAuth bool
}

type fakeIssuer struct {
	*httptest.Server

	mu            sync.Mutex
	down          bool
	issuer        string
	expiresIn     int
	headerOnly    bool
	discoveries   int
	tokenRequests []tokenRequest
}

func newFakeIssuer(t *testing.T) *fakeIssuer {
	f := &fakeIssuer{expiresIn: 3600}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", f.serveConfiguration)
	mux.HandleFunc("POST /token", f.serveToken)
	f.Server = httptest.NewServer(mux)
	f.issuer = f.URL
	t.Cleanup(f.Close)
	return f
}

func (f *fakeIssuer) serveConfiguration(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	f.discoveries++
	err := json.NewEncoder(w).Encode(
		map[string]string{
			"issuer":         f.issuer,
			"token_endpoint": f.URL + "/token",
		},
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (f *fakeIssuer) serveToken(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	id, secret, headerAuth := r.BasicAuth()
	if !headerAuth {
		id, secret = r.FormValue("client_id"), r.FormValue("client_secret")
	}
	f.tokenRequests = append(f.tokenRequests, tokenRequest{resource: r.FormValue("resource"), headerAuth: headerAuth})

	w.Header().Set("Content-Type", "application/json")
	switch {
	case f.headerOnly && !headerAuth:
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
		return
	case f.headerOnly && headerAuth:
		// Refused for a reason other than how the client authenticated.
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_target"}`))
		return
	case id != "client" || secret != "hunter2" || r.FormValue("grant_type") != "client_credentials":
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
		return
	}
	err := json.NewEncoder(w).Encode(
		struct {
			AccessToken string `json:"access_token"`
			TokenType   string `json:"token_type"`
			ExpiresIn   int    `json:"expires_in"`
		}{
			AccessToken: fmt.Sprintf("token-%d-for-%s", len(f.tokenRequests), r.FormValue("resource")),
			TokenType:   "Bearer",
			ExpiresIn:   f.expiresIn,
		},
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (f *fakeIssuer) set(fn func(f *fakeIssuer)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakeIssuer) counts() (discoveries int, tokenRequests []tokenRequest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.discoveries, append([]tokenRequest(nil), f.tokenRequests...)
}

func newClient(t *testing.T, issuer string, opts ...Option) *Client {
	c, err := New(issuer, "client", "hunter2", opts...)
	require.NoError(t, err)
	return c
}

func TestNew_RejectsBadConfiguration(t *testing.T) {
	_, err := New("id.example", "client", "hunter2")
	require.Error(t, err, "issuer without scheme")

	_, err = New("https://id.example", "", "hunter2")
	require.Error(t, err, "missing client ID")

	_, err = New("https://id.example", "client", "")
	require.Error(t, err, "missing client secret")
}

func TestClient_DiscoversLazily(t *testing.T) {
	issuer := newFakeIssuer(t)
	c := newClient(t, issuer.URL)

	discoveries, _ := issuer.counts()
	assert.Zero(t, discoveries, "New should not contact the issuer")

	_, err := c.Token(context.Background(), "https://a.example")
	require.NoError(t, err)
	_, err = c.Token(context.Background(), "https://b.example")
	require.NoError(t, err)

	discoveries, _ = issuer.counts()
	assert.Equal(t, 1, discoveries, "discovery should be shared between resources")
}

func TestClient_StartsWhileIssuerDown(t *testing.T) {
	issuer := newFakeIssuer(t)
	issuer.set(func(f *fakeIssuer) { f.down = true })

	c := newClient(t, issuer.URL)
	_, err := c.Token(context.Background(), "https://api.example")
	require.Error(t, err)

	issuer.set(func(f *fakeIssuer) { f.down = false })
	token, err := c.Token(context.Background(), "https://api.example")
	require.NoError(t, err, "a failed discovery should be retried")
	assert.Equal(t, "token-1-for-https://api.example", token.AccessToken)
}

func TestClient_StartsWhileIssuerUnreachable(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	server.Close()

	_, err := newClient(t, server.URL).Token(context.Background(), "https://api.example")
	require.Error(t, err)
}

func TestClient_CachesTokenPerResource(t *testing.T) {
	issuer := newFakeIssuer(t)
	c := newClient(t, issuer.URL)

	for range 2 {
		token, err := c.Token(context.Background(), "https://a.example")
		require.NoError(t, err)
		assert.Equal(t, "token-1-for-https://a.example", token.AccessToken)
	}
	token, err := c.Token(context.Background(), "https://b.example")
	require.NoError(t, err)
	assert.Equal(t, "token-2-for-https://b.example", token.AccessToken)

	_, requests := issuer.counts()
	assert.Len(t, requests, 2)
}

func TestClient_RefreshesExpiredToken(t *testing.T) {
	issuer := newFakeIssuer(t)
	issuer.set(func(f *fakeIssuer) { f.expiresIn = 1 })
	c := newClient(t, issuer.URL)

	token, err := c.Token(context.Background(), "https://api.example")
	require.NoError(t, err)
	assert.Equal(t, "token-1-for-https://api.example", token.AccessToken)

	issuer.set(func(f *fakeIssuer) { f.expiresIn = 3600 })
	token, err = c.Token(context.Background(), "https://api.example")
	require.NoError(t, err)
	assert.Equal(t, "token-2-for-https://api.example", token.AccessToken)

	token, err = c.Token(context.Background(), "https://api.example")
	require.NoError(t, err)
	assert.Equal(t, "token-2-for-https://api.example", token.AccessToken)

	discoveries, _ := issuer.counts()
	assert.Equal(t, 1, discoveries, "refreshing should reuse the discovered endpoint")
}

func TestClient_RetriesFailedTokenRequest(t *testing.T) {
	issuer := newFakeIssuer(t)
	issuer.set(func(f *fakeIssuer) { f.headerOnly = true })
	c := newClient(t, issuer.URL, WithHeaderAuth())

	_, err := c.Token(context.Background(), "https://api.example")
	require.Error(t, err)

	issuer.set(func(f *fakeIssuer) { f.headerOnly = false })
	_, err = c.Token(context.Background(), "https://api.example")
	require.NoError(t, err)
}

func TestClient_RejectsMismatchedIssuer(t *testing.T) {
	issuer := newFakeIssuer(t)
	issuer.set(func(f *fakeIssuer) { f.issuer = "https://elsewhere.example" })

	_, err := newClient(t, issuer.URL).Token(context.Background(), "https://api.example")
	require.ErrorContains(t, err, "elsewhere.example")
	_, requests := issuer.counts()
	assert.Empty(t, requests)
}

func TestClient_AcceptsTrailingSlashOnIssuer(t *testing.T) {
	_, err := newClient(t, newFakeIssuer(t).URL+"/").Token(context.Background(), "https://api.example")
	require.NoError(t, err)
}

func TestClient_EmptyResourceSendsNoParameter(t *testing.T) {
	token, err := newClient(t, newFakeIssuer(t).URL).Token(context.Background(), "")
	require.NoError(t, err)
	assert.Equal(t, "token-1-for-", token.AccessToken)
}

func TestWithHTTPClient(t *testing.T) {
	issuer := newFakeIssuer(t)
	var requests int
	_, err := newClient(
		t,
		issuer.URL,
		WithHTTPClient(
			&http.Client{
				Transport: roundTripFunc(
					func(r *http.Request) (*http.Response, error) {
						requests++
						return http.DefaultTransport.RoundTrip(r)
					},
				),
			},
		),
	).Token(context.Background(), "https://api.example")
	require.NoError(t, err)
	assert.Equal(t, 2, requests, "discovery and token requests should both use the client")
}

func TestWithHeaderAuth(t *testing.T) {
	issuer := newFakeIssuer(t)
	issuer.set(func(f *fakeIssuer) { f.headerOnly = true })

	_, err := newClient(t, issuer.URL).Token(context.Background(), "https://api.example")
	var retrieveErr *oauth2.RetrieveError
	require.ErrorAs(t, err, &retrieveErr)
	assert.Equal(t, "invalid_client", retrieveErr.ErrorCode, "without the option, the fallback's error is reported")

	issuer.set(func(f *fakeIssuer) { f.tokenRequests = nil })
	_, err = newClient(t, issuer.URL, WithHeaderAuth()).Token(context.Background(), "https://api.example")
	require.ErrorAs(t, err, &retrieveErr)
	assert.Equal(t, "invalid_target", retrieveErr.ErrorCode)

	_, requests := issuer.counts()
	assert.Equal(t, []tokenRequest{{resource: "https://api.example", headerAuth: true}}, requests)
}

func TestWithTrimmedResources(t *testing.T) {
	issuer := newFakeIssuer(t)

	token, err := newClient(t, issuer.URL).Token(context.Background(), "https://api.example/")
	require.NoError(t, err)
	assert.Equal(t, "token-1-for-https://api.example/", token.AccessToken, "resources are sent as given by default")

	c := newClient(t, issuer.URL, WithTrimmedResources())
	token, err = c.Token(context.Background(), "https://api.example//")
	require.NoError(t, err)
	assert.Equal(t, "token-2-for-https://api.example", token.AccessToken)

	token, err = c.Token(context.Background(), "https://api.example")
	require.NoError(t, err)
	assert.Equal(t, "token-2-for-https://api.example", token.AccessToken, "trimmed and untrimmed should share a token")
}

func TestTokenFunc(t *testing.T) {
	c := newClient(t, newFakeIssuer(t).URL)
	tokenFunc := c.TokenFunc("https://api.example")

	for range 2 {
		token, err := tokenFunc(context.Background())
		require.NoError(t, err)
		assert.Equal(t, "token-1-for-https://api.example", token)
	}

	token, err := c.Token(context.Background(), "https://api.example")
	require.NoError(t, err)
	assert.Equal(t, "token-1-for-https://api.example", token.AccessToken, "all shapes should share the cache")
}

func TestTokenFunc_UsesCallerContext(t *testing.T) {
	c := newClient(t, newFakeIssuer(t).URL)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := c.TokenFunc("https://api.example")(ctx)
	require.ErrorIs(t, err, context.Canceled)

	_, err = c.TokenFunc("https://api.example")(context.Background())
	require.NoError(t, err, "a cancelled caller should not break later requests")
}

func TestTokenSource(t *testing.T) {
	source := newClient(t, newFakeIssuer(t).URL).TokenSource("https://api.example")

	for range 2 {
		token, err := source.Token()
		require.NoError(t, err)
		assert.Equal(t, "token-1-for-https://api.example", token.AccessToken)
	}
}

func TestTransport(t *testing.T) {
	issuer := newFakeIssuer(t)
	api := httptest.NewServer(
		http.HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(r.Header.Get("Authorization")))
			},
		),
	)
	t.Cleanup(api.Close)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, api.URL, nil)
	require.NoError(t, err)
	res, err := (&http.Client{Transport: newClient(t, issuer.URL).Transport(api.URL, nil)}).Do(req)
	require.NoError(t, err)
	defer func() { _ = res.Body.Close() }()

	body, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	assert.Equal(t, "Bearer token-1-for-"+api.URL, string(body))
}

func TestTransport_FailsWithoutToken(t *testing.T) {
	issuer := newFakeIssuer(t)
	issuer.set(func(f *fakeIssuer) { f.down = true })
	var sent bool
	base := roundTripFunc(
		func(*http.Request) (*http.Response, error) {
			sent = true
			return nil, errors.New("should not be called")
		},
	)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://api.example", nil)
	require.NoError(t, err)
	res, err := newClient(t, issuer.URL).Transport("https://api.example", base).RoundTrip(req)
	if res != nil {
		_ = res.Body.Close()
	}
	require.ErrorContains(t, err, "discovering token endpoint")
	assert.False(t, sent)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
