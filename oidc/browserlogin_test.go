package oidc

import (
	"context"
	"encoding/base64"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"chameth.com/x/internal/oidctest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testBase = "https://service.example"

type subjectKey struct{}

type loginHarness struct {
	t        *testing.T
	provider *oidctest.Provider
	login    *BrowserLogin
	handler  http.Handler
	now      time.Time
	cookies  map[string]*http.Cookie
	token    string
	header   http.Header
}

func newLoginHarness(t *testing.T, change func(*BrowserLoginConfig)) *loginHarness {
	t.Helper()
	now := time.Date(2026, 11, 8, 14, 0, 0, 0, time.UTC)
	p := oidctest.New(t, now)

	cfg := BrowserLoginConfig{
		Issuer:         p.Issuer(),
		ClientID:       oidctest.ClientID,
		ClientSecret:   oidctest.ClientSecret,
		SessionKey:     strings.Repeat("k", 32),
		BaseURL:        testBase + "/",
		ExemptPrefixes: []string{"/static"},
		ExemptPaths:    []string{"/metrics", "/api/hooks/x"},
		WithSubject: func(ctx context.Context, subject string) context.Context {
			return context.WithValue(ctx, subjectKey{}, subject)
		},
	}
	if change != nil {
		change(&cfg)
	}
	l, err := NewBrowserLogin(cfg)
	require.NoError(t, err)

	h := &loginHarness{
		t:        t,
		provider: p,
		login:    l,
		now:      now,
		cookies:  map[string]*http.Cookie{},
		header:   http.Header{},
	}
	l.now = func() time.Time { return h.now }
	h.handler = l.Wrap(
		http.HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) {
				subject, _ := r.Context().Value(subjectKey{}).(string)
				_, _ = w.Write([]byte(subject + " " + r.URL.RequestURI()))
			},
		),
	)
	return h
}

// do sends a request with the cookies the harness holds, and keeps any the
// response sets, the way a browser would.
func (h *loginHarness) do(method, target string) *httptest.ResponseRecorder {
	h.t.Helper()
	req := httptest.NewRequestWithContext(h.t.Context(), method, target, nil)
	for _, c := range h.cookies {
		req.AddCookie(c)
	}
	if h.token != "" {
		req.Header.Set("Authorization", "Bearer "+h.token)
	}
	maps.Copy(req.Header, h.header)
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	for _, c := range rec.Result().Cookies() {
		if c.MaxAge < 0 || (!c.Expires.IsZero() && !c.Expires.After(h.now)) {
			delete(h.cookies, c.Name)
		} else {
			h.cookies[c.Name] = c
		}
	}
	return rec
}

// startLogin requests target without a session and plays the browser's
// visit to the provider, returning the state to come back with.
func (h *loginHarness) startLogin(target string) string {
	h.t.Helper()
	rec := h.do(http.MethodGet, target)
	require.Equal(h.t, http.StatusFound, rec.Code)
	return h.provider.Authorize(rec.Header().Get("Location")).Get("state")
}

func (h *loginHarness) callback(state string) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.do(http.MethodGet, CallbackPath+"?code="+oidctest.Code+"&state="+url.QueryEscape(state))
}

func (h *loginHarness) logIn() *httptest.ResponseRecorder {
	h.t.Helper()
	return h.callback(h.startLogin("/"))
}

func TestNewBrowserLogin_requiresConfig(t *testing.T) {
	valid := BrowserLoginConfig{
		Issuer:       "https://id.example",
		ClientID:     "c",
		ClientSecret: "s",
		SessionKey:   strings.Repeat("k", 32),
		BaseURL:      testBase,
	}
	baseURLFunc := func(context.Context) (string, error) { return testBase, nil }
	tests := map[string]func(*BrowserLoginConfig){
		"no issuer":            func(c *BrowserLoginConfig) { c.Issuer = "" },
		"no client ID":         func(c *BrowserLoginConfig) { c.ClientID = "" },
		"no client secret":     func(c *BrowserLoginConfig) { c.ClientSecret = "" },
		"short session key":    func(c *BrowserLoginConfig) { c.SessionKey = strings.Repeat("k", 31) },
		"no base URL":          func(c *BrowserLoginConfig) { c.BaseURL = "" },
		"two base URLs":        func(c *BrowserLoginConfig) { c.BaseURLFunc = baseURLFunc },
		"relative base URL":    func(c *BrowserLoginConfig) { c.BaseURL = "service.example" },
		"relative exempt path": func(c *BrowserLoginConfig) { c.ExemptPaths = []string{"metrics"} },
		"unclean exempt path":  func(c *BrowserLoginConfig) { c.ExemptPaths = []string{"/a/../metrics"} },
		"root exempt prefix":   func(c *BrowserLoginConfig) { c.ExemptPrefixes = []string{"/"} },
		"trailing slash":       func(c *BrowserLoginConfig) { c.ExemptPrefixes = []string{"/static/"} },
		"unclean bearer path":  func(c *BrowserLoginConfig) { c.BearerPrefix = "/api/" },
		"base URL with a path": func(c *BrowserLoginConfig) { c.BaseURL = testBase + "/app" },
		"base URL with query":  func(c *BrowserLoginConfig) { c.BaseURL = testBase + "?x=1" },
		"base URL with ?":      func(c *BrowserLoginConfig) { c.BaseURL = testBase + "?" },
		"base URL fragment":    func(c *BrowserLoginConfig) { c.BaseURL = testBase + "#top" },
		"escaped exempt path":  func(c *BrowserLoginConfig) { c.ExemptPaths = []string{"/a b"} },
		"escaped prefix":       func(c *BrowserLoginConfig) { c.ExemptPrefixes = []string{"/café"} },
		"escaped bearer path":  func(c *BrowserLoginConfig) { c.BearerPrefix = "/my api" },
	}
	for name, change := range tests {
		t.Run(
			name,
			func(t *testing.T) {
				cfg := valid
				change(&cfg)
				_, err := NewBrowserLogin(cfg)
				assert.Error(t, err)
			},
		)
	}
	_, err := NewBrowserLogin(valid)
	assert.NoError(t, err)
}

func TestBrowserLogin_roundTripReturnsToTheOriginalPage(t *testing.T) {
	h := newLoginHarness(t, nil)

	rec := h.do(http.MethodGet, "/things/1?tab=logs")
	require.Equal(t, http.StatusFound, rec.Code)
	query := h.provider.Authorize(rec.Header().Get("Location"))
	assert.Equal(t, oidctest.ClientID, query.Get("client_id"))
	assert.Equal(t, testBase+CallbackPath, query.Get("redirect_uri"))
	assert.Equal(t, "S256", query.Get("code_challenge_method"))
	assert.Equal(t, "openid", query.Get("scope"))
	assert.NotEmpty(t, query.Get("nonce"))

	rec = h.callback(query.Get("state"))

	require.Equal(t, http.StatusFound, rec.Code)
	assert.Equal(t, "/things/1?tab=logs", rec.Header().Get("Location"))
	session := h.cookies[sessionCookie]
	require.NotNil(t, session)
	assert.True(t, session.HttpOnly)
	assert.True(t, session.Secure)
	assert.Equal(t, http.SameSiteLaxMode, session.SameSite)
	assert.Equal(t, "/", session.Path)
	assert.Len(t, h.cookies, 1, "the state cookie should be cleared once used")

	rec = h.do(http.MethodGet, "/things/1?tab=logs")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, oidctest.Subject+" /things/1?tab=logs", rec.Body.String())
}

func TestBrowserLogin_longReturnURLsAreCutBack(t *testing.T) {
	tests := map[string]struct {
		target string
		want   string
	}{
		"query dropped":     {"/things?" + strings.Repeat("a=1&", 1500), "/things"},
		"path dropped":      {"/" + strings.Repeat("x", 5000) + "?a=1", "/"},
		"short URL is kept": {"/things?a=1&b=2", "/things?a=1&b=2"},
	}
	for name, tt := range tests {
		t.Run(
			name,
			func(t *testing.T) {
				h := newLoginHarness(t, nil)
				state := h.startLogin(tt.target)
				cookie := h.cookies[stateCookiePrefix+state]
				require.NotNil(t, cookie)
				assert.LessOrEqual(t, len(cookie.Name)+len(cookie.Value), maxCookieSize)

				rec := h.callback(state)

				require.Equal(t, http.StatusFound, rec.Code)
				assert.Equal(t, tt.want, rec.Header().Get("Location"))
			},
		)
	}
}

func TestBrowserLogin_severalTabsLogInAtOnce(t *testing.T) {
	h := newLoginHarness(t, nil)

	first := h.startLogin("/one")

	// The provider remembers the last challenge, so finish the second first,
	// then replay the first's authorization to finish that too.
	rec := h.callback(h.startLogin("/two"))
	require.Equal(t, http.StatusFound, rec.Code)
	assert.Equal(t, "/two", rec.Header().Get("Location"))

	delete(h.cookies, sessionCookie)
	rec = h.do(http.MethodGet, "/one")
	require.Equal(t, http.StatusFound, rec.Code)
	third := h.provider.Authorize(rec.Header().Get("Location")).Get("state")
	assert.NotEqual(t, first, third)
	assert.Contains(t, h.cookies, stateCookiePrefix+first, "an unfinished login keeps its own cookie")
	rec = h.callback(third)
	require.Equal(t, http.StatusFound, rec.Code)
	assert.Equal(t, "/one", rec.Header().Get("Location"))
}

func TestBrowserLogin_sessionLastsUntilFiveInTheMorningByDefault(t *testing.T) {
	h := newLoginHarness(t, nil)
	h.logIn()

	// 14:00 UTC on 8 November is 14:00 in London; the session runs to 05:00 on
	// the 9th, regardless of the ID token's one-minute expiry.
	assert.Equal(t, time.Date(2026, 11, 9, 5, 0, 0, 0, time.UTC), h.cookies[sessionCookie].Expires.UTC())

	h.now = time.Date(2026, 11, 9, 4, 59, 0, 0, time.UTC)
	assert.Equal(t, http.StatusOK, h.do(http.MethodGet, "/").Code)

	// Send the cookie past its expiry, to check the server does not take the
	// browser's word for it.
	session := h.cookies[sessionCookie]
	h.now = time.Date(2026, 11, 9, 5, 0, 0, 0, time.UTC)
	h.cookies[sessionCookie] = session
	assert.Equal(t, http.StatusFound, h.do(http.MethodGet, "/").Code)
}

func TestBrowserLogin_customExpiry(t *testing.T) {
	h := newLoginHarness(t, func(c *BrowserLoginConfig) { c.Expiry = ExpireAfter(time.Hour) })
	h.logIn()

	assert.Equal(t, h.now.Add(time.Hour), h.cookies[sessionCookie].Expires.UTC())

	session := h.cookies[sessionCookie]
	h.now = h.now.Add(time.Hour)
	h.cookies[sessionCookie] = session
	assert.Equal(t, http.StatusFound, h.do(http.MethodGet, "/").Code)
}

func TestBrowserLogin_expiryInThePastFailsTheLogin(t *testing.T) {
	h := newLoginHarness(t, func(c *BrowserLoginConfig) { c.Expiry = ExpireAfter(0) })

	assert.Equal(t, http.StatusInternalServerError, h.logIn().Code)
	assert.NotContains(t, h.cookies, sessionCookie)
}

func TestExpireDailyAt_acrossClockChanges(t *testing.T) {
	london, err := time.LoadLocation("Europe/London")
	require.NoError(t, err)
	expiry := ExpireDailyAt(5, london)

	// The clocks go forward at 01:00 UTC on 29 March 2026 and back at 01:00 UTC
	// on 25 October 2026.
	assert.Equal(
		t,
		time.Date(2026, 3, 29, 4, 0, 0, 0, time.UTC),
		expiry(time.Date(2026, 3, 28, 22, 0, 0, 0, time.UTC)).UTC(),
	)
	assert.Equal(
		t,
		time.Date(2026, 10, 25, 5, 0, 0, 0, time.UTC),
		expiry(time.Date(2026, 10, 24, 22, 0, 0, 0, time.UTC)).UTC(),
	)
	assert.Equal(
		t,
		time.Date(2026, 11, 9, 5, 0, 0, 0, time.UTC),
		expiry(time.Date(2026, 11, 9, 4, 59, 0, 0, time.UTC)).UTC(),
		"a login before 05:00 lasts until that morning's",
	)
	assert.Equal(
		t,
		time.Date(2026, 11, 10, 5, 0, 0, 0, time.UTC),
		expiry(time.Date(2026, 11, 9, 5, 0, 0, 0, time.UTC)).UTC(),
		"a login at exactly 05:00 lasts until the next day's",
	)
}

func TestBrowserLogin_exemptPaths(t *testing.T) {
	h := newLoginHarness(t, nil)

	for _, p := range []string{"/static/icon.svg", "/static", "/static/", "/metrics", "/api/hooks/x"} {
		rec := h.do(http.MethodGet, p)
		assert.Equal(t, http.StatusOK, rec.Code, p)
		assert.Equal(t, " "+p, rec.Body.String(), "exempt requests have no subject: %s", p)
	}
	assert.Equal(t, http.StatusOK, h.do(http.MethodPost, "/metrics").Code, "exempt paths take any method")

	for _, p := range []string{
		"/",
		"/things",
		"/metrics/x",
		"/metrics/",
		"/staticfoo",
		"/static/../things",
		"/static/./icon.svg",
		"//static/icon.svg",
		"/static/..%2Fthings",
		"/static%2F..%2Fthings",
		"/things/%2E%2E%2Fmetrics",
		"/things/..%2F..%2Fmetrics",
		"/%73tatic/icon.svg",
	} {
		assert.Equal(t, http.StatusFound, h.do(http.MethodGet, p).Code, p)
	}
}

func TestBrowserLogin_nonNavigationIsNotRedirected(t *testing.T) {
	h := newLoginHarness(t, nil)
	assert.Equal(t, http.StatusUnauthorized, h.do(http.MethodPost, "/").Code)

	h.header.Set("Sec-Fetch-Mode", "cors")
	assert.Equal(t, http.StatusUnauthorized, h.do(http.MethodGet, "/").Code)

	h.header.Set("Sec-Fetch-Mode", "navigate")
	assert.Equal(t, http.StatusFound, h.do(http.MethodGet, "/").Code)
}

func TestBrowserLogin_rejectsCrossOriginRequestsWithASession(t *testing.T) {
	h := newLoginHarness(t, nil)
	h.logIn()

	h.header.Set("Sec-Fetch-Site", "cross-site")
	assert.Equal(t, http.StatusForbidden, h.do(http.MethodPost, "/things").Code)
	assert.Equal(t, http.StatusOK, h.do(http.MethodGet, "/things").Code)

	h.header.Set("Sec-Fetch-Site", "same-origin")
	assert.Equal(t, http.StatusOK, h.do(http.MethodPost, "/things").Code)
}

func TestBrowserLogin_ignoresBearerTokensUnlessConfigured(t *testing.T) {
	h := newLoginHarness(t, nil)
	h.token = h.provider.AccessToken(testBase, h.now, nil)

	assert.Equal(t, http.StatusFound, h.do(http.MethodGet, "/api/things").Code)
}

func TestBrowserLogin_bearerPrefixNeedsAnAccessToken(t *testing.T) {
	h := newLoginHarness(t, func(c *BrowserLoginConfig) { c.BearerPrefix = "/api" })

	paths := []string{"/api/things", "/api/things/1", "/api"}
	for _, p := range paths {
		rec := h.do(http.MethodGet, p)
		assert.Equal(t, http.StatusUnauthorized, rec.Code, p)
		assert.Equal(t, "Bearer", rec.Header().Get("WWW-Authenticate"), p)
	}

	h.token = h.provider.AccessToken(testBase, h.now, nil)
	for _, p := range paths {
		rec := h.do(http.MethodGet, p)
		assert.Equal(t, http.StatusOK, rec.Code, p)
		assert.Equal(t, "service-caller "+p, rec.Body.String(), p)
	}
	assert.Equal(t, http.StatusFound, h.do(http.MethodGet, "/").Code, "a token is not a login for the pages")
	assert.Equal(t, http.StatusOK, h.do(http.MethodPost, "/api/hooks/x").Code, "exempt paths beat the prefix")

	h.token = h.provider.AccessToken("https://other.example", h.now, nil)
	rec := h.do(http.MethodGet, "/api/things")
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, `Bearer error="invalid_token"`, rec.Header().Get("WWW-Authenticate"))
}

func TestBrowserLogin_bearerPrefixDoesNotAcceptASession(t *testing.T) {
	h := newLoginHarness(t, func(c *BrowserLoginConfig) { c.BearerPrefix = "/api" })
	h.logIn()

	rec := h.do(http.MethodGet, "/api/things")
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, "Bearer", rec.Header().Get("WWW-Authenticate"))
}

func TestBrowserLogin_bearerPrefixIsNotFooledByEscapes(t *testing.T) {
	h := newLoginHarness(t, func(c *BrowserLoginConfig) { c.BearerPrefix = "/api" })
	h.token = h.provider.AccessToken(testBase, h.now, nil)

	for _, p := range []string{"/things/%2E%2E%2Fapi%2Fthings", "/api/../things", "/%61pi/things"} {
		assert.Equal(t, http.StatusFound, h.do(http.MethodGet, p).Code, p)
	}
}

func TestBrowserLogin_bearerAnywhere(t *testing.T) {
	h := newLoginHarness(t, func(c *BrowserLoginConfig) { c.BearerAnywhere = true })
	assert.Equal(t, http.StatusFound, h.do(http.MethodGet, "/things").Code, "pages still need a login without a token")

	h.token = h.provider.AccessToken(testBase, h.now, nil)
	rec := h.do(http.MethodPost, "/things")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "service-caller /things", rec.Body.String())

	h.token = "nonsense"
	assert.Equal(t, http.StatusUnauthorized, h.do(http.MethodGet, "/things").Code)
}

func TestBrowserLogin_providerDownIsRetried(t *testing.T) {
	h := newLoginHarness(t, func(c *BrowserLoginConfig) { c.BearerPrefix = "/api" })
	h.provider.SetDown(true)
	assert.Equal(t, http.StatusBadGateway, h.do(http.MethodGet, "/").Code)

	h.token = h.provider.AccessToken(testBase, h.now, nil)
	assert.Equal(t, http.StatusBadGateway, h.do(http.MethodGet, "/api/things").Code)

	h.provider.SetDown(false)
	assert.Equal(t, http.StatusOK, h.do(http.MethodGet, "/api/things").Code)
	assert.Equal(t, http.StatusFound, h.do(http.MethodGet, "/").Code)
}

func TestBrowserLogin_keysDownIsUnavailable(t *testing.T) {
	h := newLoginHarness(t, nil)
	state := h.startLogin("/things")
	h.provider.SetKeysDown(true)
	assert.Equal(t, http.StatusBadGateway, h.callback(state).Code)
	assert.NotContains(t, h.cookies, sessionCookie)

	h.provider.SetKeysDown(false)
	rec := h.logIn()
	require.Equal(t, http.StatusFound, rec.Code)
	assert.Contains(t, h.cookies, sessionCookie)
}

func TestBrowserLogin_baseURLFunc(t *testing.T) {
	calls := 0
	h := newLoginHarness(
		t,
		func(c *BrowserLoginConfig) {
			c.BaseURL = ""
			c.BaseURLFunc = func(context.Context) (string, error) {
				calls++
				if calls == 1 {
					return "", errors.New("not serving yet")
				}
				return "http://localhost:8080", nil
			}
		},
	)
	assert.Equal(t, http.StatusBadGateway, h.do(http.MethodGet, "/").Code)

	rec := h.do(http.MethodGet, "/")
	require.Equal(t, http.StatusFound, rec.Code)
	query := h.provider.Authorize(rec.Header().Get("Location"))
	assert.Equal(t, "http://localhost:8080"+CallbackPath, query.Get("redirect_uri"))

	h.callback(query.Get("state"))
	require.Contains(t, h.cookies, sessionCookie)
	assert.False(t, h.cookies[sessionCookie].Secure, "cookies over plain HTTP can't be Secure")
}

func TestBrowserLogin_callbackRejects(t *testing.T) {
	tests := map[string]struct {
		code   int
		target func(h *loginHarness) string
		method string
	}{
		"an unknown state": {
			code:   http.StatusBadRequest,
			target: func(*loginHarness) string { return CallbackPath + "?code=" + oidctest.Code + "&state=x" },
		},
		"no state": {
			code:   http.StatusBadRequest,
			target: func(*loginHarness) string { return CallbackPath + "?code=" + oidctest.Code },
		},
		"an expired login": {
			code: http.StatusBadRequest,
			target: func(h *loginHarness) string {
				state := h.startLogin("/")
				h.now = h.now.Add(loginTimeout)
				return CallbackPath + "?code=" + oidctest.Code + "&state=" + state
			},
		},
		"a wrong code": {
			code:   http.StatusBadGateway,
			target: func(h *loginHarness) string { return CallbackPath + "?code=wrong&state=" + h.startLogin("/") },
		},
		"a mismatched nonce": {
			code: http.StatusForbidden,
			target: func(h *loginHarness) string {
				state := h.startLogin("/")
				h.provider.SetNonce("other")
				return CallbackPath + "?code=" + oidctest.Code + "&state=" + state
			},
		},
		"a refusal": {
			code:   http.StatusForbidden,
			target: func(h *loginHarness) string { return CallbackPath + "?error=access_denied&state=" + h.startLogin("/") },
		},
		"a POST": {
			code: http.StatusMethodNotAllowed,
			target: func(h *loginHarness) string {
				return CallbackPath + "?code=" + oidctest.Code + "&state=" + h.startLogin("/")
			},
			method: http.MethodPost,
		},
	}
	for name, tt := range tests {
		t.Run(
			name,
			func(t *testing.T) {
				h := newLoginHarness(t, nil)
				method := http.MethodGet
				if tt.method != "" {
					method = tt.method
				}
				assert.Equal(t, tt.code, h.do(method, tt.target(h)).Code)
				assert.NotContains(t, h.cookies, sessionCookie)
			},
		)
	}
}

func TestBrowserLogin_stateCannotBeReused(t *testing.T) {
	h := newLoginHarness(t, nil)
	state := h.startLogin("/")
	stateCookie := h.cookies[stateCookiePrefix+state]
	require.Equal(t, http.StatusFound, h.callback(state).Code)

	delete(h.cookies, sessionCookie)
	assert.Equal(t, http.StatusBadRequest, h.callback(state).Code, "the browser drops the cleared state cookie")

	h.cookies[stateCookie.Name] = stateCookie
	assert.Equal(t, http.StatusBadGateway, h.callback(state).Code, "the provider won't take the code twice")
}

func TestBrowserLogin_rejectsTamperedCookies(t *testing.T) {
	t.Run(
		"a changed subject",
		func(t *testing.T) {
			h := newLoginHarness(t, nil)
			h.logIn()
			session := h.cookies[sessionCookie]
			encoded, sig, _ := strings.Cut(session.Value, ".")
			payload, err := base64.RawURLEncoding.DecodeString(encoded)
			require.NoError(t, err)
			session.Value = base64.RawURLEncoding.EncodeToString(
				[]byte(strings.Replace(string(payload), oidctest.Subject, "user-2", 1)),
			) + "." + sig

			assert.Equal(t, http.StatusFound, h.do(http.MethodGet, "/").Code)
		},
	)
	t.Run(
		"a pending login used as a session",
		func(t *testing.T) {
			h := newLoginHarness(t, nil)
			state := h.startLogin("/")
			h.cookies[sessionCookie] = &http.Cookie{
				Name:  sessionCookie,
				Value: h.cookies[stateCookiePrefix+state].Value,
			}

			assert.Equal(t, http.StatusFound, h.do(http.MethodGet, "/").Code)
		},
	)
	t.Run(
		"a session signed with another key",
		func(t *testing.T) {
			other := newLoginHarness(t, func(c *BrowserLoginConfig) { c.SessionKey = strings.Repeat("j", 32) })
			other.logIn()
			h := newLoginHarness(t, nil)
			h.cookies[sessionCookie] = other.cookies[sessionCookie]

			assert.Equal(t, http.StatusFound, h.do(http.MethodGet, "/").Code)
		},
	)
}

func TestLocalPath(t *testing.T) {
	for _, p := range []string{"https://evil.example/", "//evil.example/", `/\evil.example`, ""} {
		assert.Equal(t, "/", localPath(p), p)
	}
	assert.Equal(t, "/things/1?tab=5", localPath("/things/1?tab=5"))
}
