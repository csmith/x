package bearer

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"chameth.com/x/internal/oidctest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testAudience = "https://service.example"

var testNow = time.Date(2026, 11, 8, 14, 0, 0, 0, time.UTC)

type subjectKey struct{}

func newVerifier(t *testing.T, p *oidctest.Provider) *Verifier {
	t.Helper()
	v, err := New(
		Config{
			Issuer:   p.Issuer(),
			Audience: testAudience,
			WithSubject: func(ctx context.Context, subject string) context.Context {
				return context.WithValue(ctx, subjectKey{}, subject)
			},
			Now: func() time.Time { return testNow },
		},
	)
	require.NoError(t, err)
	return v
}

func serve(t *testing.T, v *Verifier, header string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/things", nil)
	if header != "" {
		r.Header.Set("Authorization", header)
	}
	rec := httptest.NewRecorder()
	v.Require(
		http.HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) {
				subject, _ := r.Context().Value(subjectKey{}).(string)
				_, _ = w.Write([]byte("hello " + subject))
			},
		),
	).ServeHTTP(rec, r)
	return rec
}

func TestNew_requiresConfig(t *testing.T) {
	audienceFunc := func(context.Context) (string, error) { return testAudience, nil }
	tests := map[string]Config{
		"no issuer":    {Audience: testAudience},
		"no audience":  {Issuer: "https://id.example"},
		"two audience": {Issuer: "https://id.example", Audience: testAudience, AudienceFunc: audienceFunc},
	}
	for name, cfg := range tests {
		t.Run(
			name,
			func(t *testing.T) {
				_, err := New(cfg)
				assert.Error(t, err)
			},
		)
	}
	_, err := New(Config{Issuer: "https://id.example", AudienceFunc: audienceFunc})
	assert.NoError(t, err)
}

func TestRequire_acceptsAValidToken(t *testing.T) {
	p := oidctest.New(t, testNow)

	rec := serve(t, newVerifier(t, p), "Bearer "+p.AccessToken(testAudience, testNow, nil))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "hello service-caller", rec.Body.String())
}

func TestRequire_acceptsTheSchemeInAnyCase(t *testing.T) {
	p := oidctest.New(t, testNow)

	assert.Equal(t, http.StatusOK, serve(t, newVerifier(t, p), "bearer "+p.AccessToken(testAudience, testNow, nil)).Code)
}

func TestRequire_challengesMissingTokens(t *testing.T) {
	v := newVerifier(t, oidctest.New(t, testNow))

	for _, header := range []string{"", "Bearer ", "Basic dXNlcjpwYXNz", "Bearer"} {
		rec := serve(t, v, header)
		assert.Equal(t, http.StatusUnauthorized, rec.Code, header)
		assert.Equal(t, "Bearer", rec.Header().Get("WWW-Authenticate"), header)
	}
}

func TestRequire_rejectsBadTokens(t *testing.T) {
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	tests := map[string]func(p *oidctest.Provider) string{
		"another service's": func(p *oidctest.Provider) string {
			return p.AccessToken("https://other.example", testNow, nil)
		},
		"another issuer's": func(p *oidctest.Provider) string {
			return p.AccessToken(testAudience, testNow, func(c map[string]any) { c["iss"] = "https://evil.example" })
		},
		"expired": func(p *oidctest.Provider) string {
			return p.AccessToken(testAudience, testNow.Add(-2*time.Hour), nil)
		},
		"an ID token": func(p *oidctest.Provider) string {
			return p.Sign(
				p.Key,
				"JWT",
				map[string]any{
					"iss": p.Issuer(),
					"sub": "user-1",
					"aud": testAudience,
					"exp": testNow.Add(time.Hour).Unix(),
				},
			)
		},
		"untyped": func(p *oidctest.Provider) string {
			return p.Sign(
				p.Key,
				"",
				map[string]any{
					"iss": p.Issuer(),
					"sub": "user-1",
					"aud": testAudience,
					"exp": testNow.Add(time.Hour).Unix(),
				},
			)
		},
		"signed by another key": func(p *oidctest.Provider) string {
			return p.Sign(
				other,
				"at+jwt",
				map[string]any{
					"iss": p.Issuer(),
					"sub": "c",
					"aud": testAudience,
					"exp": testNow.Add(time.Hour).Unix(),
				},
			)
		},
		"not a JWT": func(*oidctest.Provider) string { return "nonsense" },
	}
	for name, token := range tests {
		t.Run(
			name,
			func(t *testing.T) {
				p := oidctest.New(t, testNow)
				rec := serve(t, newVerifier(t, p), "Bearer "+token(p))
				assert.Equal(t, http.StatusUnauthorized, rec.Code)
				assert.Equal(t, `Bearer error="invalid_token"`, rec.Header().Get("WWW-Authenticate"))
			},
		)
	}
}

func TestVerify_acceptsTheApplicationTypeForm(t *testing.T) {
	p := oidctest.New(t, testNow)
	v := newVerifier(t, p)
	raw := p.Sign(
		p.Key,
		"application/at+jwt",
		map[string]any{
			"iss": p.Issuer(),
			"sub": "c",
			"aud": []string{"https://other.example", testAudience},
			"exp": testNow.Add(time.Hour).Unix(),
		},
	)

	token, err := v.Verify(t.Context(), raw)

	require.NoError(t, err)
	assert.Equal(t, "c", token.Subject)
	assert.Equal(t, testNow.Add(time.Hour), token.Expiry.UTC())
}

func TestVerify_exposesClaims(t *testing.T) {
	p := oidctest.New(t, testNow)

	token, err := newVerifier(t, p).Verify(
		t.Context(),
		p.AccessToken(testAudience, testNow, func(c map[string]any) { c["role"] = "x" }),
	)

	require.NoError(t, err)
	var claims struct {
		Role string `json:"role"`
	}
	require.NoError(t, token.Claims(&claims))
	assert.Equal(t, "x", claims.Role)
}

func TestRequire_providerDownIsRetried(t *testing.T) {
	p := oidctest.New(t, testNow)
	v := newVerifier(t, p)
	header := "Bearer " + p.AccessToken(testAudience, testNow, nil)

	p.SetDown(true)
	assert.Equal(t, http.StatusBadGateway, serve(t, v, header).Code)

	p.SetDown(false)
	assert.Equal(t, http.StatusOK, serve(t, v, header).Code)
}

func TestVerify_audienceFuncFailureIsUnavailable(t *testing.T) {
	p := oidctest.New(t, testNow)
	calls := 0
	v, err := New(
		Config{
			Issuer: p.Issuer(),
			AudienceFunc: func(context.Context) (string, error) {
				calls++
				if calls == 1 {
					return "", errors.New("not serving yet")
				}
				return testAudience, nil
			},
			Now: func() time.Time { return testNow },
		},
	)
	require.NoError(t, err)
	raw := p.AccessToken(testAudience, testNow, nil)

	_, err = v.Verify(t.Context(), raw)
	require.ErrorIs(t, err, ErrUnavailable)

	_, err = v.Verify(t.Context(), raw)
	assert.NoError(t, err)
}
