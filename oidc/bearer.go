package oidc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

// ErrBearerUnavailable is wrapped by errors from Verify when the provider could
// not be discovered or its signing keys could not be fetched, so the token was
// not fully checked.
var ErrBearerUnavailable = errors.New("token verification is unavailable")

type BearerConfig struct {
	// Issuer is the OpenID Connect issuer URL the tokens must come from.
	Issuer string
	// Audience is the audience tokens must name, normally the service's URL.
	// Exactly one of Audience and AudienceFunc must be set.
	Audience string
	// AudienceFunc gives the audience on first use, for services that only
	// know their URL once they are serving. It is called again after a
	// failure.
	AudienceFunc func(context.Context) (string, error)
	// WithSubject, if set, is given the request context and the token's
	// subject by Require, and returns the context the wrapped handler sees.
	WithSubject func(ctx context.Context, subject string) context.Context
	// Now is the clock tokens are checked against. It defaults to time.Now.
	Now func() time.Time
}

// BearerVerifier checks RFC 9068 access tokens sent as
// "Authorization: Bearer <token>".
//
// The provider is discovered on first use rather than at construction, so a
// service still starts while its provider is down, and a failed discovery is
// tried again on the next request. Signing keys are cached, and fetched again
// when a token is signed with a key not yet seen, so tokens are otherwise
// checked without contacting the provider.
type BearerVerifier struct {
	issuer      string
	audience    func(context.Context) (string, error)
	withSubject func(context.Context, string) context.Context
	now         func() time.Time

	mu       sync.Mutex
	verifier *tokenVerifier
}

// NewBearerVerifier checks cfg and returns a BearerVerifier for it. The
// provider is not contacted until the first token is verified.
func NewBearerVerifier(cfg BearerConfig) (*BearerVerifier, error) {
	if cfg.Issuer == "" {
		return nil, errors.New("the token issuer must be set")
	}
	if (cfg.Audience == "") == (cfg.AudienceFunc == nil) {
		return nil, errors.New("exactly one of the token audience and audience func must be set")
	}
	v := &BearerVerifier{
		issuer:      cfg.Issuer,
		audience:    cfg.AudienceFunc,
		withSubject: cfg.WithSubject,
		now:         cfg.Now,
	}
	if cfg.Audience != "" {
		v.audience = func(context.Context) (string, error) { return cfg.Audience, nil }
	}
	if v.now == nil {
		v.now = time.Now
	}
	return v, nil
}

// Require passes requests to next only with a valid token. A request without
// one gets a 401 with a WWW-Authenticate challenge, never a redirect. If the
// provider can't be reached it gets a 502 instead, since that isn't the
// caller's fault and a 401 might send it off to fetch a new token.
func (v *BearerVerifier) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			raw, ok := BearerToken(r)
			if !ok {
				w.Header().Set("WWW-Authenticate", "Bearer")
				http.Error(w, "An access token is required", http.StatusUnauthorized)
				return
			}
			token, err := v.Verify(r.Context(), raw)
			if errors.Is(err, ErrBearerUnavailable) {
				slog.Error("Failed to set up access token verification", "error", err)
				http.Error(w, "Token verification is unavailable", http.StatusBadGateway)
				return
			}
			if err != nil {
				slog.Warn("Rejected access token", "error", err, "path", r.URL.Path)
				w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
				http.Error(w, "Invalid access token", http.StatusUnauthorized)
				return
			}
			if v.withSubject != nil {
				r = r.WithContext(v.withSubject(r.Context(), token.Subject))
			}
			next.ServeHTTP(w, r)
		},
	)
}

// BearerToken returns the token from r's Authorization header, and whether
// there was a non-empty one using the Bearer scheme.
func BearerToken(r *http.Request) (string, bool) {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	return token, token != ""
}

// AccessToken is a verified access token.
type AccessToken struct {
	Subject string
	Expiry  time.Time
	idToken *oidc.IDToken
}

// Claims decodes the token's claims into v.
func (t *AccessToken) Claims(v any) error {
	return t.idToken.Claims(v)
}

// Verify checks raw is an access token from the issuer for the audience. The
// error wraps ErrBearerUnavailable if the provider could not be discovered or
// its signing keys could not be fetched.
//
// A token is accepted if it is a JWT of type at+jwt, signed by one of the
// issuer's keys, issued by the issuer, unexpired, and with the configured
// audience (normally the service's own URL) among its audiences. There are no
// scopes: any such token is accepted. The type check is what stops an ID
// token, which is signed by the same keys, standing in for an access token.
func (v *BearerVerifier) Verify(ctx context.Context, raw string) (*AccessToken, error) {
	verifier, err := v.discover(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBearerUnavailable, err)
	}
	id, err := verifier.Verify(ctx, raw)
	if errors.Is(err, errKeysUnavailable) {
		return nil, fmt.Errorf("%w: %w", ErrBearerUnavailable, err)
	}
	if err != nil {
		return nil, err
	}
	if err := checkType(raw); err != nil {
		return nil, err
	}
	return &AccessToken{Subject: id.Subject, Expiry: id.Expiry, idToken: id}, nil
}

func (v *BearerVerifier) discover(ctx context.Context) (*tokenVerifier, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.verifier != nil {
		return v.verifier, nil
	}
	audience, err := v.audience(ctx)
	if err != nil {
		return nil, fmt.Errorf("work out token audience: %w", err)
	}
	provider, err := oidc.NewProvider(providerContext(ctx), v.issuer)
	if err != nil {
		return nil, fmt.Errorf("discover OIDC provider: %w", err)
	}
	v.verifier, err = newTokenVerifier(provider, v.issuer, oidc.Config{ClientID: audience, Now: v.now})
	if err != nil {
		return nil, err
	}
	return v.verifier, nil
}

// providerContext gives go-oidc's requests a timeout, since discovery holds a
// lock that every caller waits on.
func providerContext(ctx context.Context) context.Context {
	return oidc.ClientContext(ctx, &http.Client{Timeout: defaultTimeout})
}

// errKeysUnavailable is wrapped by errors from tokenVerifier.Verify when the
// provider's signing keys could not be fetched, so the token was not checked.
var errKeysUnavailable = errors.New("the provider's signing keys could not be fetched")

// tokenVerifier verifies JWTs signed with the provider's keys, telling a
// failure to fetch the keys apart from a bad token.
type tokenVerifier struct {
	verifier *oidc.IDTokenVerifier
}

func newTokenVerifier(provider *oidc.Provider, issuer string, config oidc.Config) (*tokenVerifier, error) {
	var metadata struct {
		KeysURL    string   `json:"jwks_uri"`
		Algorithms []string `json:"id_token_signing_alg_values_supported"`
	}
	if err := provider.Claims(&metadata); err != nil {
		return nil, fmt.Errorf("read OIDC provider metadata: %w", err)
	}
	config.SupportedSigningAlgs = metadata.Algorithms
	return &tokenVerifier{
		verifier: oidc.NewVerifier(
			issuer,
			reportingKeySet{oidc.NewRemoteKeySet(providerContext(context.Background()), metadata.KeysURL)},
			&config,
		),
	}, nil
}

func (v *tokenVerifier) Verify(ctx context.Context, raw string) (*oidc.IDToken, error) {
	var fetchErr error
	id, err := v.verifier.Verify(context.WithValue(ctx, keyFetchErrorKey{}, &fetchErr), raw)
	if fetchErr != nil {
		return nil, fmt.Errorf("%w: %w", errKeysUnavailable, fetchErr)
	}
	return id, err
}

type keyFetchErrorKey struct{}

// reportingKeySet records a failure to fetch the provider's signing keys in
// the *error under keyFetchErrorKey in the context. go-oidc's verifier turns
// every signature error into the same string, so a fetch failure can't be
// told apart from a bad signature from the error Verify returns.
type reportingKeySet struct {
	keys *oidc.RemoteKeySet
}

func (k reportingKeySet) VerifySignature(ctx context.Context, jwt string) ([]byte, error) {
	payload, err := k.keys.VerifySignature(ctx, jwt)
	// A failed fetch is the only error the remote key set returns with a
	// wrapped cause; a signature no key matches is a plain error.
	if err != nil && errors.Unwrap(err) != nil {
		if fetchErr, ok := ctx.Value(keyFetchErrorKey{}).(*error); ok {
			*fetchErr = err
		}
	}
	return payload, err
}

// checkType requires the RFC 9068 type in the JWT header, in either of the
// forms the RFC allows.
func checkType(raw string) error {
	encoded, _, _ := strings.Cut(raw, ".")
	header, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return fmt.Errorf("decode token header: %w", err)
	}
	var h struct {
		Type string `json:"typ"`
	}
	if err := json.Unmarshal(header, &h); err != nil {
		return fmt.Errorf("parse token header: %w", err)
	}
	if t := strings.ToLower(h.Type); t != "at+jwt" && t != "application/at+jwt" {
		return fmt.Errorf("token type %q is not an access token", h.Type)
	}
	return nil
}
