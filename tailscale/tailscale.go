// Package tailscale sets up tsnet nodes and serves HTTP handlers on them.
package tailscale

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tsnet"
)

// NewServer returns a tsnet server with the given hostname, state directory
// and auth key, logging through slog. Messages meant for the user (such as
// login URLs) are logged at info level, and backend chatter at debug level.
// Empty dir and authKey leave tsnet's defaults in place.
//
// Without [WithLogger], it logs through the default logger as it is when
// NewServer is called, with a component=tailscale attribute.
func NewServer(hostname, dir, authKey string, opts ...Option) *tsnet.Server {
	o := serverOptions{logger: slog.Default().With("component", "tailscale")}
	for _, opt := range opts {
		opt(&o)
	}
	return &tsnet.Server{
		Hostname: hostname,
		Dir:      dir,
		AuthKey:  authKey,
		UserLogf: logf(o.logger, slog.LevelInfo),
		Logf:     logf(o.logger, slog.LevelDebug),
	}
}

// Option configures [NewServer].
type Option func(*serverOptions)

type serverOptions struct {
	logger *slog.Logger
}

// WithLogger logs through logger as given, adding no attributes.
func WithLogger(logger *slog.Logger) Option {
	return func(o *serverOptions) { o.logger = logger }
}

// Upper is the part of [tsnet.Server] that [Up] uses.
type Upper interface {
	Up(ctx context.Context) (*ipnstate.Status, error)
}

// Up brings the node up, waiting for it to log in if it has never joined the
// tailnet, and returns its full MagicDNS name without the trailing dot.
func Up(ctx context.Context, s Upper) (string, error) {
	status, err := s.Up(ctx)
	if err != nil {
		return "", fmt.Errorf("join tailnet: %w", err)
	}
	if status.Self == nil || status.Self.DNSName == "" {
		return "", errors.New("join tailnet: the node has no DNS name")
	}
	return strings.TrimSuffix(status.Self.DNSName, "."), nil
}

// Listener is the part of [tsnet.Server] that [Serve] uses.
type Listener interface {
	Listen(network, addr string) (net.Listener, error)
	ListenTLS(network, addr string) (net.Listener, error)
}

const shutdownTimeout = 10 * time.Second

// Serve serves handler over HTTPS on port 443 of s, and redirects plain HTTP
// on port 80 to the same path at https://name, until ctx is done. name should
// be the node's full MagicDNS name, as returned by [Up], as that's the name on
// the certificate.
//
// When ctx is done the servers are shut down gracefully, giving in-flight
// requests a few seconds to finish before their connections are closed. Serve
// returns nil after a clean shutdown, or the error that stopped it otherwise.
func Serve(ctx context.Context, s Listener, name string, handler http.Handler) error {
	httpListener, err := s.Listen("tcp", ":80")
	if err != nil {
		return fmt.Errorf("listen for http: %w", err)
	}
	httpsListener, err := s.ListenTLS("tcp", ":443")
	if err != nil {
		_ = httpListener.Close()
		return fmt.Errorf("listen for https: %w", err)
	}

	slog.Info("Serving on the tailnet", "url", "https://"+name)
	return serveAll(
		ctx,
		listening{newHTTPServer(redirectToHTTPS(name)), httpListener},
		listening{newHTTPServer(handler), httpsListener},
	)
}

type listening struct {
	server   *http.Server
	listener net.Listener
}

func serveAll(ctx context.Context, servers ...listening) error {
	errs := make(chan error, len(servers))
	for _, s := range servers {
		go func() { errs <- s.server.Serve(s.listener) }()
	}

	var failed error
	select {
	case failed = <-errs:
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	for _, s := range servers {
		if err := s.server.Shutdown(shutdownCtx); err != nil {
			_ = s.server.Close()
			if failed == nil {
				failed = fmt.Errorf("shut down: %w", err)
			}
		}
	}
	if failed != nil && !errors.Is(failed, http.ErrServerClosed) {
		return failed
	}
	return nil
}

// There's no read or write timeout, as they'd cut off slow uploads and
// long-lived responses such as server-sent events.
func newHTTPServer(handler http.Handler) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
}

func redirectToHTTPS(name string) http.Handler {
	return http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "https://"+name+r.URL.RequestURI(), http.StatusMovedPermanently)
		},
	)
}

func logf(logger *slog.Logger, level slog.Level) func(format string, args ...any) {
	return func(format string, args ...any) {
		if !logger.Enabled(context.Background(), level) {
			return
		}
		msg := strings.TrimSuffix(fmt.Sprintf(format, args...), "\n")
		logger.Log(context.Background(), level, msg)
	}
}
