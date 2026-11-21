package oidc

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // the default session expiry is in Europe/London, and containers often lack zoneinfo

	xhttp "chameth.com/x/http"
	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

const (
	// CallbackPath is where the provider sends the browser back to. Register
	// the service's URL with this path appended as the client's callback URL.
	CallbackPath = "/auth/callback"

	sessionCookie     = "auth_session"
	stateCookiePrefix = "auth_state_"
	loginTimeout      = 10 * time.Minute
	// maxCookieSize is the most a cookie's name and value can come to before
	// browsers drop it.
	maxCookieSize = 4096
)

type BrowserLoginConfig struct {
	// Issuer is the OpenID Connect issuer URL.
	Issuer       string
	ClientID     string
	ClientSecret string
	// SessionKey signs the cookies, and must be at least
	// xhttp.MinCookieKeyLength bytes. Changing it logs everyone out.
	SessionKey string

	// BaseURL is the service's own URL: the base of the callback URL, and the
	// audience bearer tokens must name. It can't have a path, query or
	// fragment, since the service must be served from the root of its host.
	// Cookies are marked Secure only if it is https. Exactly one of BaseURL
	// and BaseURLFunc must be set.
	BaseURL string
	// BaseURLFunc gives the base URL on first use, for services that only know
	// their URL once they are serving. It is called again after a failure.
	BaseURLFunc func(context.Context) (string, error)

	// Expiry gives when a session started at the given time ends. It defaults
	// to the next 05:00 Europe/London, so a session runs out overnight rather
	// than while a form is half filled in.
	Expiry SessionExpiry

	// ExemptPrefixes are paths that need neither a login nor a token, along
	// with everything under them: "/static" opens "/static" and
	// "/static/app.css", but not "/staticfoo".
	ExemptPrefixes []string
	// ExemptPaths are matched exactly: "/metrics" opens that path and nothing
	// under it.
	ExemptPaths []string

	// BearerPrefix, if set, is a path that, with everything under it, takes
	// only bearer tokens and not sessions, such as "/api". A request there
	// without a valid token gets a 401 and never a redirect.
	BearerPrefix string
	// BearerAnywhere accepts a bearer token in place of a session on any path:
	// a request with a Bearer Authorization header is judged on the token
	// alone.
	BearerAnywhere bool

	// WithSubject, if set, is given the request context and the subject of
	// the session or bearer token, and returns the context the wrapped handler
	// sees.
	WithSubject func(ctx context.Context, subject string) context.Context
}

// BrowserLogin puts a service's web pages behind an OpenID Connect login, using
// the authorization-code flow with PKCE. It requires a session, or a token
// where configured, on everything but the exempt paths.
//
// There is no session store. A session is a cookie signed with the session
// key, so it survives restarts and deploys as long as the key stays the same,
// and it lasts as long as the configured Expiry says, whatever the ID token's
// own expiry. While a login is in progress, what the callback needs is kept
// in a cookie named after the login's state, so several tabs sent to log in
// at once (a browser restoring its tabs after the session ran out, say) each
// find their own.
//
// Optionally, some or all paths also take RFC 9068 access tokens sent as
// "Authorization: Bearer <token>", with the service's URL as their audience,
// for other services calling an API; see [BearerVerifier].
//
// Requests let through with a session are also protected against cross-origin
// requests by [http.CrossOriginProtection], so another site can't make
// changes using the browser's session cookie.
type BrowserLogin struct {
	issuer         string
	clientID       string
	clientSecret   string
	cookies        *xhttp.CookieSigner
	baseURL        func(context.Context) (string, error)
	expiry         SessionExpiry
	exemptPrefixes []string
	exemptPaths    []string
	bearerPrefix   string
	bearerAnywhere bool
	withSubject    func(context.Context, string) context.Context
	bearer         *BearerVerifier
	crossOrigin    *http.CrossOriginProtection
	now            func() time.Time

	mu        sync.Mutex
	discovery *discovery
}

// discovery is everything built from the provider's discovery document and
// the base URL.
type discovery struct {
	oauth  *oauth2.Config
	id     *tokenVerifier
	secure bool
}

// NewBrowserLogin checks cfg and returns a BrowserLogin for it. The provider is
// not contacted until the first request that needs it, so a service still
// starts while its provider is down, and a failed discovery is tried again on
// the next request.
//
// Exempt paths and the bearer prefix must be absolute and already clean, as
// path.Clean leaves them, not the root, and need no percent-encoding. A request for any other spelling of
// a path, such as "/static/../admin" or "/x/%2E%2E%2Fmetrics", is never exempt
// and never counted as under the bearer prefix.
func NewBrowserLogin(cfg BrowserLoginConfig) (*BrowserLogin, error) {
	if cfg.Issuer == "" || cfg.ClientID == "" || cfg.ClientSecret == "" {
		return nil, errors.New("the OIDC issuer, client ID and client secret must all be set")
	}
	cookies, err := xhttp.NewCookieSigner([]byte(cfg.SessionKey))
	if err != nil {
		return nil, fmt.Errorf("session key: %w", err)
	}
	baseURL, err := baseURLFunc(cfg.BaseURL, cfg.BaseURLFunc)
	if err != nil {
		return nil, err
	}
	for _, p := range slices.Concat(cfg.ExemptPrefixes, cfg.ExemptPaths) {
		if err := checkPath(p); err != nil {
			return nil, fmt.Errorf("exempt path: %w", err)
		}
	}
	if cfg.BearerPrefix != "" {
		if err := checkPath(cfg.BearerPrefix); err != nil {
			return nil, fmt.Errorf("bearer prefix: %w", err)
		}
	}

	l := &BrowserLogin{
		issuer:         cfg.Issuer,
		clientID:       cfg.ClientID,
		clientSecret:   cfg.ClientSecret,
		cookies:        cookies,
		baseURL:        baseURL,
		expiry:         cfg.Expiry,
		exemptPrefixes: cfg.ExemptPrefixes,
		exemptPaths:    cfg.ExemptPaths,
		bearerPrefix:   cfg.BearerPrefix,
		bearerAnywhere: cfg.BearerAnywhere,
		withSubject:    cfg.WithSubject,
		crossOrigin:    http.NewCrossOriginProtection(),
		now:            time.Now,
	}
	if l.expiry == nil {
		london, err := time.LoadLocation("Europe/London")
		if err != nil {
			return nil, fmt.Errorf("load session expiry time zone: %w", err)
		}
		l.expiry = ExpireDailyAt(5, london)
	}
	if cfg.BearerPrefix != "" || cfg.BearerAnywhere {
		l.bearer, err = NewBearerVerifier(
			BearerConfig{
				Issuer:       cfg.Issuer,
				AudienceFunc: baseURL,
				WithSubject:  cfg.WithSubject,
				Now:          func() time.Time { return l.now() },
			},
		)
		if err != nil {
			return nil, err
		}
	}
	return l, nil
}

// Wrap serves CallbackPath itself, and passes other requests to next once
// they are logged in, exempt, or carry a valid token where one is accepted.
//
// A browser loading a page without a session is sent off to log in, and
// brought back to the same page afterwards. Anything else without a session,
// such as a POST or a script's fetch, gets a 401 instead: a redirect would
// bring a POST back as a GET without its body, and a script can't follow one
// to the provider.
func (l *BrowserLogin) Wrap(next http.Handler) http.Handler {
	sessionNext := l.crossOrigin.Handler(next)
	return http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.URL.Path == CallbackPath && r.URL.EscapedPath() == CallbackPath:
				if r.Method != http.MethodGet {
					http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
					return
				}
				l.handleCallback(w, r)
			case l.isExempt(r.URL):
				next.ServeHTTP(w, r)
			case l.bearerPrefix != "" && isUnder(r.URL, l.bearerPrefix), l.bearerAnywhere && hasBearer(r):
				l.bearer.Require(next).ServeHTTP(w, r)
			default:
				l.requireSession(sessionNext, w, r)
			}
		},
	)
}

func (l *BrowserLogin) requireSession(next http.Handler, w http.ResponseWriter, r *http.Request) {
	var s session
	if l.cookies.Read(r, sessionCookie, &s) && l.now().Before(s.Expires) {
		if l.withSubject != nil {
			r = r.WithContext(l.withSubject(r.Context(), s.Subject))
		}
		next.ServeHTTP(w, r)
		return
	}
	if !isNavigation(r) {
		http.Error(w, "Not logged in", http.StatusUnauthorized)
		return
	}
	l.redirectToProvider(w, r)
}

// isNavigation reports whether r is the browser loading a page, rather than
// a script fetching something or a form being sent. Clients that don't say
// which are taken to be loading a page.
func isNavigation(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	mode := r.Header.Get("Sec-Fetch-Mode")
	return mode == "" || mode == "navigate"
}

func hasBearer(r *http.Request) bool {
	scheme, _, _ := strings.Cut(r.Header.Get("Authorization"), " ")
	return strings.EqualFold(scheme, "Bearer")
}

// The decoded and escaped paths must both match, since a mux may route on
// either: "/x/%2E%2E%2Fmetrics" must not pass as "/metrics".
func (l *BrowserLogin) isExempt(u *url.URL) bool {
	return l.exemptPath(u.Path) && l.exemptPath(u.EscapedPath())
}

func (l *BrowserLogin) exemptPath(p string) bool {
	if !canonical(p) {
		return false
	}
	return slices.Contains(l.exemptPaths, p) ||
		slices.ContainsFunc(l.exemptPrefixes, func(prefix string) bool { return under(p, prefix) })
}

func isUnder(u *url.URL, prefix string) bool {
	return canonical(u.Path) && canonical(u.EscapedPath()) && under(u.Path, prefix) && under(u.EscapedPath(), prefix)
}

func canonical(p string) bool {
	clean := path.Clean(p)
	return p == clean || p == clean+"/"
}

func under(p, prefix string) bool {
	return p == prefix || strings.HasPrefix(p, prefix+"/")
}

type session struct {
	Subject string    `json:"sub"`
	Expires time.Time `json:"exp"`
}

// pending is what the callback needs to finish a login.
type pending struct {
	Verifier string    `json:"verifier"`
	Nonce    string    `json:"nonce"`
	Return   string    `json:"return"`
	Expires  time.Time `json:"exp"`
}

func (l *BrowserLogin) redirectToProvider(w http.ResponseWriter, r *http.Request) {
	d, err := l.discover(r.Context())
	if err != nil {
		slog.Error("Failed to set up login", "error", err)
		http.Error(w, "Login is unavailable", http.StatusBadGateway)
		return
	}

	state := rand.Text()
	p := pending{
		Verifier: oauth2.GenerateVerifier(),
		Nonce:    rand.Text(),
		Expires:  l.now().Add(loginTimeout),
	}
	name := stateCookiePrefix + state
	var value string
	// A browser drops a cookie that's too big, which would leave the callback
	// without it, so a long return URL is cut back to its path, then the root.
	for _, ret := range []string{r.URL.RequestURI(), r.URL.EscapedPath(), "/"} {
		p.Return = ret
		var err error
		value, err = l.cookies.Seal(name, p)
		if err != nil {
			slog.Error("Failed to start login", "error", err)
			http.Error(w, "Login is unavailable", http.StatusInternalServerError)
			return
		}
		if len(name)+len(value) <= maxCookieSize {
			break
		}
	}
	http.SetCookie(w, d.cookie(name, value, CallbackPath, p.Expires))
	http.Redirect(
		w,
		r,
		d.oauth.AuthCodeURL(state, oauth2.S256ChallengeOption(p.Verifier), oidc.Nonce(p.Nonce)),
		http.StatusFound,
	)
}

func (l *BrowserLogin) handleCallback(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	name := stateCookiePrefix + query.Get("state")
	var p pending
	if query.Get("state") == "" || !l.cookies.Read(r, name, &p) || !l.now().Before(p.Expires) {
		http.Error(w, "This login has expired; go back to the page and try again", http.StatusBadRequest)
		return
	}

	d, err := l.discover(r.Context())
	if err != nil {
		slog.Error("Failed to set up login", "error", err)
		http.Error(w, "Login is unavailable", http.StatusBadGateway)
		return
	}
	http.SetCookie(w, d.cookie(name, "", CallbackPath, time.Unix(0, 0)))

	if e := query.Get("error"); e != "" {
		slog.Warn("Login refused by provider", "error", e, "description", query.Get("error_description"))
		http.Error(w, "Login was refused", http.StatusForbidden)
		return
	}

	token, err := d.oauth.Exchange(r.Context(), query.Get("code"), oauth2.VerifierOption(p.Verifier))
	if err != nil {
		slog.Error("Failed to exchange login code", "error", err)
		http.Error(w, "Login failed", http.StatusBadGateway)
		return
	}
	raw, ok := token.Extra("id_token").(string)
	if !ok {
		slog.Error("Login token response had no ID token")
		http.Error(w, "Login failed", http.StatusBadGateway)
		return
	}
	id, err := d.id.Verify(r.Context(), raw)
	if errors.Is(err, errKeysUnavailable) {
		slog.Error("Failed to check login ID token", "error", err)
		http.Error(w, "Login is unavailable", http.StatusBadGateway)
		return
	}
	if err != nil || id.Nonce != p.Nonce {
		slog.Warn("Rejected login ID token", "error", err)
		http.Error(w, "Login failed", http.StatusForbidden)
		return
	}

	now := l.now()
	s := session{Subject: id.Subject, Expires: l.expiry(now)}
	if !s.Expires.After(now) {
		slog.Error("Session expiry policy gave a time that has already passed", "expires", s.Expires)
		http.Error(w, "Login failed", http.StatusInternalServerError)
		return
	}
	value, err := l.cookies.Seal(sessionCookie, s)
	if err != nil {
		slog.Error("Failed to create session", "error", err)
		http.Error(w, "Login failed", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, d.cookie(sessionCookie, value, "/", s.Expires))
	http.Redirect(w, r, localPath(p.Return), http.StatusFound)
}

// discover finds the provider and the base URL on first use and keeps the
// result. A failure is not kept, so the next request tries again.
func (l *BrowserLogin) discover(ctx context.Context) (*discovery, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.discovery != nil {
		return l.discovery, nil
	}

	base, err := l.baseURL(ctx)
	if err != nil {
		return nil, err
	}
	provider, err := oidc.NewProvider(providerContext(ctx), l.issuer)
	if err != nil {
		return nil, fmt.Errorf("discover OIDC provider: %w", err)
	}
	id, err := newTokenVerifier(provider, l.issuer, oidc.Config{ClientID: l.clientID, Now: l.now})
	if err != nil {
		return nil, err
	}
	l.discovery = &discovery{
		oauth: &oauth2.Config{
			ClientID:     l.clientID,
			ClientSecret: l.clientSecret,
			Endpoint:     provider.Endpoint(),
			RedirectURL:  base + CallbackPath,
			Scopes:       []string{oidc.ScopeOpenID},
		},
		id:     id,
		secure: strings.HasPrefix(base, "https://"),
	}
	return l.discovery, nil
}

func (d *discovery) cookie(name, value, path string, expires time.Time) *http.Cookie {
	return &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     path,
		Expires:  expires,
		HttpOnly: true,
		Secure:   d.secure,
		// Lax rather than Strict: the provider's redirect back to the callback
		// is a cross-site navigation, and it has to carry the state cookie.
		SameSite: http.SameSiteLaxMode,
	}
}

// baseURLFunc turns the configured base URL into a function that checks and
// normalises it.
func baseURLFunc(base string, fn func(context.Context) (string, error)) (func(context.Context) (string, error), error) {
	if (base == "") == (fn == nil) {
		return nil, errors.New("exactly one of the base URL and base URL func must be set")
	}
	if fn == nil {
		normalised, err := checkBaseURL(base)
		if err != nil {
			return nil, err
		}
		return func(context.Context) (string, error) { return normalised, nil }, nil
	}
	return func(ctx context.Context) (string, error) {
		base, err := fn(ctx)
		if err != nil {
			return "", fmt.Errorf("work out base URL: %w", err)
		}
		return checkBaseURL(base)
	}, nil
}

func checkBaseURL(base string) (string, error) {
	base = strings.TrimSuffix(base, "/")
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("the base URL %q must be an absolute http or https URL", base)
	}
	if u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", fmt.Errorf("the base URL %q must not have a path, query or fragment", base)
	}
	return base, nil
}

func checkPath(p string) error {
	if !strings.HasPrefix(p, "/") || p == "/" || path.Clean(p) != p {
		return fmt.Errorf("%q must be absolute and clean, and not the root", p)
	}
	// Requests are matched on their escaped path as well as the decoded one,
	// so a path that needs escaping could never match.
	if (&url.URL{Path: p}).EscapedPath() != p {
		return fmt.Errorf("%q must not need percent-encoding", p)
	}
	return nil
}

// localPath keeps the redirect after a login on this service. The stored
// value is signed, so this is a second check rather than the only one.
func localPath(p string) string {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.HasPrefix(p, `/\`) {
		return "/"
	}
	return p
}

// SessionExpiry gives the time a session started at loggedIn ends, which must
// be after loggedIn.
type SessionExpiry func(loggedIn time.Time) time.Time

// ExpireDailyAt ends sessions at the first hour:00 in loc strictly after login.
// It is still hour:00 on the days the clocks change, unless that hour is
// skipped or repeated.
func ExpireDailyAt(hour int, loc *time.Location) SessionExpiry {
	return func(t time.Time) time.Time {
		local := t.In(loc)
		reset := time.Date(local.Year(), local.Month(), local.Day(), hour, 0, 0, 0, loc)
		if !reset.After(local) {
			reset = time.Date(local.Year(), local.Month(), local.Day()+1, hour, 0, 0, 0, loc)
		}
		return reset
	}
}

// ExpireAfter ends sessions a fixed duration after login.
func ExpireAfter(d time.Duration) SessionExpiry {
	return func(t time.Time) time.Time {
		return t.Add(d)
	}
}
