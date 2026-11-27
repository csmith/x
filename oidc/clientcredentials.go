// Package oidc authenticates with an OpenID Connect provider: services getting
// access tokens for other services' APIs, services checking the access tokens
// they're sent, and people logging in to a service's web pages.
package oidc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

const defaultTimeout = 30 * time.Second

// ClientCredentials gets OAuth 2.0 access tokens with the client credentials
// grant, from one provider as one OAuth client, for any number of APIs
// identified by an RFC 8707 resource parameter. It is safe for concurrent use.
//
// The provider's token endpoint is discovered when the first token is needed,
// not at construction, so a service whose provider is down still starts; a
// failed discovery is tried again on the next request. Each resource's token is
// cached until shortly before it expires, and then fetched again.
type ClientCredentials struct {
	issuer        string
	clientID      string
	clientSecret  string
	httpClient    *http.Client
	authStyle     oauth2.AuthStyle
	trimResources bool

	discoveryMu sync.Mutex
	tokenURL    string

	tokensMu sync.Mutex
	tokens   map[string]*resourceToken
}

// ClientCredentialsOption configures a ClientCredentials.
type ClientCredentialsOption func(*ClientCredentials)

// WithHTTPClient sets the client used for discovery and token requests. It
// should have a timeout, as a token request blocks every other request for the
// same resource. The default client times out after 30 seconds.
func WithHTTPClient(client *http.Client) ClientCredentialsOption {
	return func(c *ClientCredentials) {
		c.httpClient = client
	}
}

// WithHeaderAuth sends the client ID and secret in the Authorization header,
// rather than detecting which style the provider accepts. Detection answers a
// refusal by retrying with the credentials in the body, and reports that
// attempt's error instead of the original one.
func WithHeaderAuth() ClientCredentialsOption {
	return func(c *ClientCredentials) {
		c.authStyle = oauth2.AuthStyleInHeader
	}
}

// WithTrimmedResources removes trailing slashes from resources before sending
// them, for providers such as Pocket ID that strip them from the identifiers
// of registered APIs.
func WithTrimmedResources() ClientCredentialsOption {
	return func(c *ClientCredentials) {
		c.trimResources = true
	}
}

// NewClientCredentials returns a ClientCredentials for the provider at issuer.
// It makes no requests, so only fails if the configuration is unusable.
func NewClientCredentials(
	issuer, clientID, clientSecret string,
	opts ...ClientCredentialsOption,
) (*ClientCredentials, error) {
	u, err := url.Parse(issuer)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("issuer %q must be an absolute http(s) URL", issuer)
	}
	if clientID == "" || clientSecret == "" {
		return nil, errors.New("client ID and secret are required")
	}

	c := &ClientCredentials{
		issuer:       issuer,
		clientID:     clientID,
		clientSecret: clientSecret,
		httpClient:   &http.Client{Timeout: defaultTimeout},
		tokens:       map[string]*resourceToken{},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// Token returns a current access token for resource, fetching a new one if the
// cached one has expired. An empty resource sends no resource parameter.
func (c *ClientCredentials) Token(ctx context.Context, resource string) (*oauth2.Token, error) {
	return c.resourceToken(resource).get(ctx)
}

// TokenFunc returns a function giving the current access token for resource,
// suitable for Temporal's client.NewAPIKeyDynamicCredentials.
func (c *ClientCredentials) TokenFunc(resource string) func(ctx context.Context) (string, error) {
	rt := c.resourceToken(resource)
	return func(ctx context.Context) (string, error) {
		token, err := rt.get(ctx)
		if err != nil {
			return "", err
		}
		return token.AccessToken, nil
	}
}

// TokenSource returns a token source for resource. Its requests run without a
// context, so are bounded only by the HTTP client's timeout.
func (c *ClientCredentials) TokenSource(resource string) oauth2.TokenSource {
	return &tokenSource{rt: c.resourceToken(resource)}
}

type tokenSource struct {
	rt *resourceToken
}

func (t *tokenSource) Token() (*oauth2.Token, error) {
	return t.rt.get(context.Background())
}

// Transport returns a round tripper that sends each request through base with
// a bearer token for resource. A nil base means http.DefaultTransport. Token
// requests run under the outgoing request's context.
func (c *ClientCredentials) Transport(resource string, base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &transport{rt: c.resourceToken(resource), base: base}
}

type transport struct {
	rt   *resourceToken
	base http.RoundTripper
}

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	token, err := t.rt.get(req.Context())
	if err != nil {
		// A round tripper must close the body even when it sends nothing.
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, err
	}
	req = req.Clone(req.Context())
	token.SetAuthHeader(req)
	return t.base.RoundTrip(req)
}

func (c *ClientCredentials) resourceToken(resource string) *resourceToken {
	if c.trimResources {
		resource = strings.TrimRight(resource, "/")
	}

	c.tokensMu.Lock()
	defer c.tokensMu.Unlock()
	if rt, ok := c.tokens[resource]; ok {
		return rt
	}
	rt := &resourceToken{client: c, resource: resource}
	c.tokens[resource] = rt
	return rt
}

type resourceToken struct {
	client   *ClientCredentials
	resource string

	mu      sync.Mutex
	current *oauth2.Token
}

func (r *resourceToken) get(ctx context.Context) (*oauth2.Token, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.current.Valid() {
		return r.current, nil
	}

	tokenURL, err := r.client.tokenEndpoint(ctx)
	if err != nil {
		return nil, err
	}
	cfg := clientcredentials.Config{
		ClientID:     r.client.clientID,
		ClientSecret: r.client.clientSecret,
		TokenURL:     tokenURL,
		AuthStyle:    r.client.authStyle,
	}
	if r.resource != "" {
		cfg.EndpointParams = url.Values{
			"resource": {r.resource},
		}
	}
	token, err := cfg.Token(context.WithValue(ctx, oauth2.HTTPClient, r.client.httpClient))
	if err != nil {
		return nil, fmt.Errorf("getting access token for %q: %w", r.resource, err)
	}
	r.current = token
	return token, nil
}

func (c *ClientCredentials) tokenEndpoint(ctx context.Context) (string, error) {
	c.discoveryMu.Lock()
	defer c.discoveryMu.Unlock()
	if c.tokenURL != "" {
		return c.tokenURL, nil
	}

	tokenURL, err := c.discover(ctx)
	if err != nil {
		return "", fmt.Errorf("discovering token endpoint for %s: %w", c.issuer, err)
	}
	c.tokenURL = tokenURL
	return tokenURL, nil
}

func (c *ClientCredentials) discover(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		strings.TrimSuffix(c.issuer, "/")+"/.well-known/openid-configuration",
		nil,
	)
	if err != nil {
		return "", err
	}
	res, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status %s", res.Status)
	}

	var doc struct {
		Issuer        string `json:"issuer"`
		TokenEndpoint string `json:"token_endpoint"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&doc); err != nil {
		return "", fmt.Errorf("decoding configuration: %w", err)
	}
	if strings.TrimSuffix(doc.Issuer, "/") != strings.TrimSuffix(c.issuer, "/") {
		return "", fmt.Errorf("configuration is for issuer %q", doc.Issuer)
	}
	if doc.TokenEndpoint == "" {
		return "", errors.New("configuration has no token endpoint")
	}
	return doc.TokenEndpoint, nil
}
