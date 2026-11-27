package oidctest

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	ClientID     = "client"
	ClientSecret = "secret"
	// Code is the only authorization code the token endpoint accepts.
	Code = "the-code"
	// Subject is who logs in through the authorization-code flow.
	Subject = "user-1"
)

// Provider is a fake OpenID Connect provider serving discovery, signing keys,
// and a token endpoint for the authorization-code flow with PKCE. The
// browser's visit to the authorization endpoint is played by Authorize rather
// than served.
type Provider struct {
	t      *testing.T
	server *httptest.Server
	// Key signs every token the provider issues.
	Key *rsa.PrivateKey

	mu        sync.Mutex
	now       time.Time
	down      bool
	keysDown  bool
	challenge string
	nonce     string
}

// New starts a provider that is closed when the test ends. Its clock starts
// at now.
func New(t *testing.T, now time.Time) *Provider {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	p := &Provider{t: t, Key: key, now: now}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", p.serveDiscovery)
	mux.HandleFunc("GET /jwks", p.serveKeys)
	mux.HandleFunc("POST /token", p.serveToken)
	p.server = httptest.NewServer(mux)
	t.Cleanup(p.server.Close)
	return p
}

// Issuer is the provider's issuer URL.
func (p *Provider) Issuer() string {
	return p.server.URL
}

// SetNow changes the clock that tokens are issued against.
func (p *Provider) SetNow(now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.now = now
}

// SetDown makes discovery fail, or work again.
func (p *Provider) SetDown(down bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.down = down
}

// SetKeysDown makes the signing keys endpoint fail, or work again, while
// discovery keeps working.
func (p *Provider) SetKeysDown(down bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.keysDown = down
}

// SetNonce changes the nonce the next ID token carries.
func (p *Provider) SetNonce(nonce string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nonce = nonce
}

// Authorize plays the browser's visit to the authorization endpoint at
// location: it remembers the PKCE challenge and nonce for the token endpoint
// and returns the query for the test to check, including the state.
func (p *Provider) Authorize(location string) url.Values {
	p.t.Helper()
	u, err := url.Parse(location)
	require.NoError(p.t, err)
	require.Equal(p.t, p.Issuer()+"/authorize", u.Scheme+"://"+u.Host+u.Path)
	q := u.Query()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.challenge = q.Get("code_challenge")
	p.nonce = q.Get("nonce")
	return q
}

// AccessToken is an RFC 9068 access token for audience, issued at now and
// valid for an hour. change, if not nil, can alter the claims before signing.
func (p *Provider) AccessToken(audience string, now time.Time, change func(claims map[string]any)) string {
	claims := map[string]any{
		"iss":       p.Issuer(),
		"sub":       "service-caller",
		"aud":       audience,
		"client_id": "service-caller",
		"iat":       now.Unix(),
		"exp":       now.Add(time.Hour).Unix(),
		"jti":       rand.Text(),
	}
	if change != nil {
		change(claims)
	}
	return p.Sign(p.Key, "at+jwt", claims)
}

// Sign makes a JWT of claims with the given type header, signed by key.
func (p *Provider) Sign(key *rsa.PrivateKey, typ jose.ContentType, claims map[string]any) string {
	p.t.Helper()
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithType(typ),
	)
	require.NoError(p.t, err)
	payload, err := json.Marshal(claims)
	require.NoError(p.t, err)
	jws, err := signer.Sign(payload)
	require.NoError(p.t, err)
	raw, err := jws.CompactSerialize()
	require.NoError(p.t, err)
	return raw
}

func (p *Provider) serveDiscovery(w http.ResponseWriter, _ *http.Request) {
	p.mu.Lock()
	down := p.down
	p.mu.Unlock()
	if down {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	p.writeJSON(
		w,
		map[string]any{
			"issuer":                                p.Issuer(),
			"authorization_endpoint":                p.Issuer() + "/authorize",
			"token_endpoint":                        p.Issuer() + "/token",
			"jwks_uri":                              p.Issuer() + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
			"token_endpoint_auth_methods_supported": []string{"client_secret_basic"},
		},
	)
}

func (p *Provider) serveKeys(w http.ResponseWriter, _ *http.Request) {
	p.mu.Lock()
	down := p.keysDown
	p.mu.Unlock()
	if down {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	p.writeJSON(
		w,
		jose.JSONWebKeySet{
			Keys: []jose.JSONWebKey{
				{Key: &p.Key.PublicKey, Algorithm: "RS256", Use: "sig"},
			},
		},
	)
}

// serveToken only answers a request with the client's credentials, Code, and
// a verifier matching the challenge Authorize last saw. Each authorization's
// code can be exchanged once.
func (p *Provider) serveToken(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	challenge, nonce, now := p.challenge, p.nonce, p.now
	p.challenge = ""
	p.mu.Unlock()

	user, pass, _ := r.BasicAuth()
	sum := sha256.Sum256([]byte(r.PostFormValue("code_verifier")))
	if challenge == "" ||
		user != ClientID ||
		pass != ClientSecret ||
		r.PostFormValue("code") != Code ||
		base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
		return
	}
	p.writeJSON(
		w,
		map[string]any{
			"access_token": "opaque",
			"token_type":   "Bearer",
			"id_token": p.Sign(
				p.Key,
				"JWT",
				map[string]any{
					"iss":   p.Issuer(),
					"sub":   Subject,
					"aud":   ClientID,
					"nonce": nonce,
					"iat":   now.Unix(),
					"exp":   now.Add(time.Minute).Unix(),
				},
			),
		},
	)
}

// writeJSON runs on the server's goroutine, so it can't stop the test there.
func (p *Provider) writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	assert.NoError(p.t, json.NewEncoder(w).Encode(v))
}
