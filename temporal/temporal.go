// Package temporal builds options for connecting to a Temporal frontend.
package temporal

import (
	"context"
	"crypto/tls"
	"log/slog"
	"net"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/log"
	"google.golang.org/grpc"
)

// ClientOptions returns options for a client of namespace on the frontend at
// address, or [client.DefaultHostPort] if address is empty, for use with
// [client.Dial] or [client.NewLazyClient].
//
// Each request carries a bearer token from token, such as one from the oidc
// package's ClientCredentials.TokenFunc. A nil token sends no credentials.
//
// The connection is plaintext unless [WithTLS] is given, even with a token,
// which would otherwise turn TLS on.
//
// Without [WithLogger], it logs through the default logger as it is when
// ClientOptions is called, with a component=temporal attribute.
func ClientOptions(
	address, namespace string,
	token func(ctx context.Context) (string, error),
	opts ...Option,
) client.Options {
	o := clientOptions{logger: slog.Default().With("component", "temporal")}
	for _, opt := range opts {
		opt(&o)
	}

	if address == "" {
		address = client.DefaultHostPort
	}
	options := client.Options{
		HostPort:  address,
		Namespace: namespace,
		Logger:    log.NewStructuredLogger(o.logger),
	}
	if token != nil {
		options.Credentials = client.NewAPIKeyDynamicCredentials(token)
	}
	if o.tls != nil {
		options.ConnectionOptions.TLS = o.tls
	} else {
		options.ConnectionOptions.TLSDisabled = true
	}
	if o.dial != nil {
		// The passthrough resolver hands address to the dialer unresolved.
		options.HostPort = "passthrough:///" + address
		options.ConnectionOptions.DialOptions = []grpc.DialOption{
			grpc.WithContextDialer(
				func(ctx context.Context, addr string) (net.Conn, error) {
					return o.dial(ctx, "tcp", addr)
				},
			),
		}
	}
	return options
}

// Option configures [ClientOptions].
type Option func(*clientOptions)

type clientOptions struct {
	logger *slog.Logger
	tls    *tls.Config
	dial   func(ctx context.Context, network, address string) (net.Conn, error)
}

// WithLogger logs through logger as given, adding no attributes.
func WithLogger(logger *slog.Logger) Option {
	return func(o *clientOptions) { o.logger = logger }
}

// WithTLS connects over TLS configured by config. A nil config uses the
// defaults, verifying the frontend against the system's root certificates.
func WithTLS(config *tls.Config) Option {
	return func(o *clientOptions) {
		if config == nil {
			config = &tls.Config{}
		}
		o.tls = config
	}
}

// WithDialer opens connections to the frontend with dial, such as a tsnet
// server's Dial, rather than resolving and dialling the address directly.
func WithDialer(dial func(ctx context.Context, network, address string) (net.Conn, error)) Option {
	return func(o *clientOptions) { o.dial = dial }
}
