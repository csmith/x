package http

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"
)

// Serve serves server on listener until ctx is done, then shuts it down
// gracefully. In-flight requests get a few seconds to finish before their
// connections are closed.
//
// Before serving, Serve sets server's ReadHeaderTimeout and IdleTimeout to
// sensible defaults if neither they nor ReadTimeout are set. It sets no read
// or write timeout, as they'd cut off slow uploads and long-lived responses
// such as server-sent events.
//
// Serve returns nil after a clean shutdown, or the error that stopped it
// otherwise.
func Serve(ctx context.Context, server *http.Server, listener net.Listener) error {
	applyDefaultTimeouts(server)

	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()

	var failed error
	select {
	case failed = <-served:
		served = nil
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		_ = server.Close()
		if failed == nil {
			failed = fmt.Errorf("shut down: %w", err)
		}
	}
	if served != nil {
		<-served
	}

	if failed != nil && !errors.Is(failed, http.ErrServerClosed) {
		return failed
	}
	return nil
}

// ListenAndServe listens on server's Addr, or ":http" if it's empty, and then
// serves it as [Serve] does.
func ListenAndServe(ctx context.Context, server *http.Server) error {
	address := server.Addr
	if address == "" {
		address = ":http"
	}
	listener, err := new(net.ListenConfig).Listen(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	return Serve(ctx, server, listener)
}

// ServeLocal serves handler over plain HTTP on address, as [Serve] does, for
// running a service locally without its usual network layer.
//
// Requests are rejected with 421 Misdirected Request unless their Host is
// address, or localhost, 127.0.0.1 or [::1] on its port, ignoring case. This
// stops pages on other sites from reaching the service through DNS
// rebinding. If address has port 0, the port actually listened on is used.
func ServeLocal(ctx context.Context, address string, handler http.Handler) error {
	listener, err := new(net.ListenConfig).Listen(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	host, _, err := net.SplitHostPort(address)
	if err != nil {
		_ = listener.Close()
		return fmt.Errorf("parse address: %w", err)
	}
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		_ = listener.Close()
		return fmt.Errorf("parse listen address: %w", err)
	}

	slog.Info("Serving locally", "address", listener.Addr().String())
	return Serve(ctx, &http.Server{Handler: checkHost(host, port, handler)}, listener)
}

func checkHost(host, port string, next http.Handler) http.Handler {
	var hosts []string
	for _, name := range []string{host, "localhost", "127.0.0.1", "[::1]"} {
		if name == "" {
			continue
		}
		hosts = append(hosts, net.JoinHostPort(strings.Trim(name, "[]"), port))
		// Clients leave the default port out of the Host header.
		if port == "80" {
			if strings.Contains(name, ":") && !strings.HasPrefix(name, "[") {
				name = "[" + name + "]"
			}
			hosts = append(hosts, name)
		}
	}
	return http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if !slices.ContainsFunc(hosts, func(h string) bool { return strings.EqualFold(h, r.Host) }) {
				http.Error(w, "Unexpected host", http.StatusMisdirectedRequest)
				return
			}
			next.ServeHTTP(w, r)
		},
	)
}

var shutdownTimeout = 10 * time.Second

const (
	defaultReadHeaderTimeout = 10 * time.Second
	defaultIdleTimeout       = 2 * time.Minute
)

// A zero ReadHeaderTimeout or IdleTimeout falls back to ReadTimeout, so a
// caller that set ReadTimeout has already chosen them.
func applyDefaultTimeouts(server *http.Server) {
	if server.ReadTimeout != 0 {
		return
	}
	if server.ReadHeaderTimeout == 0 {
		server.ReadHeaderTimeout = defaultReadHeaderTimeout
	}
	if server.IdleTimeout == 0 {
		server.IdleTimeout = defaultIdleTimeout
	}
}
