package temporal

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"math/big"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestClientOptions_connects(t *testing.T) {
	for i := range 8 {
		useDialer, useTLS, useToken := i&4 != 0, i&2 != 0, i&1 != 0
		t.Run(
			fmt.Sprintf("dialer=%t,tls=%t,token=%t", useDialer, useTLS, useToken),
			func(t *testing.T) { testConnects(t, useDialer, useTLS, useToken) },
		)
	}
}

func testConnects(t *testing.T, useDialer, useTLS, useToken bool) {
	frontend := newFakeFrontend(t, useTLS)
	dialer := &recordingDialer{}

	var opts []Option
	if useDialer {
		opts = append(opts, WithDialer(dialer.dial))
	}
	if useTLS {
		opts = append(opts, WithTLS(frontend.clientTLS))
	}
	var token func(context.Context) (string, error)
	if useToken {
		token = func(context.Context) (string, error) { return "secret-token", nil }
	}

	options := ClientOptions(frontend.address, "my-namespace", token, opts...)
	md := frontend.call(t, options)

	if useToken {
		assert.Equal(t, []string{"Bearer secret-token"}, md.Get("authorization"))
	} else {
		assert.Empty(t, md.Get("authorization"))
	}
	assert.Equal(t, "my-namespace", options.Namespace)

	if useDialer {
		assert.Equal(t, "tcp "+frontend.address, dialer.first())
	} else {
		assert.Equal(t, frontend.address, options.HostPort)
	}
}

func TestClientOptions_disablesTLSByDefault(t *testing.T) {
	options := ClientOptions(
		"frontend:7233",
		"my-namespace",
		func(context.Context) (string, error) { return "secret-token", nil },
	)
	assert.True(t, options.ConnectionOptions.TLSDisabled)
	assert.Nil(t, options.ConnectionOptions.TLS)
}

func TestClientOptions_defaultsEmptyAddressWithDialer(t *testing.T) {
	options := ClientOptions("", "my-namespace", nil, WithDialer(new(net.Dialer).DialContext))
	assert.Equal(t, "passthrough:///"+client.DefaultHostPort, options.HostPort)
}

func TestWithTLS_nilUsesDefaults(t *testing.T) {
	options := ClientOptions("frontend:7233", "my-namespace", nil, WithTLS(nil))
	assert.False(t, options.ConnectionOptions.TLSDisabled)
	assert.NotNil(t, options.ConnectionOptions.TLS)
}

// recordingDialer dials directly, recording the network and address of each
// connection.
type recordingDialer struct {
	mu     sync.Mutex
	dialed []string
}

func (r *recordingDialer) dial(ctx context.Context, network, address string) (net.Conn, error) {
	r.mu.Lock()
	r.dialed = append(r.dialed, network+" "+address)
	r.mu.Unlock()
	return new(net.Dialer).DialContext(ctx, network, address)
}

func (r *recordingDialer) first() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.dialed) == 0 {
		return ""
	}
	return r.dialed[0]
}

// fakeFrontend accepts any gRPC call, recording the first call's metadata and
// refusing it.
type fakeFrontend struct {
	address   string
	clientTLS *tls.Config
	calls     chan metadata.MD
}

func newFakeFrontend(t *testing.T, useTLS bool) *fakeFrontend {
	listener, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	f := &fakeFrontend{address: listener.Addr().String(), calls: make(chan metadata.MD, 1)}
	serverOpts := []grpc.ServerOption{
		grpc.UnknownServiceHandler(
			func(_ any, stream grpc.ServerStream) error {
				md, _ := metadata.FromIncomingContext(stream.Context())
				select {
				case f.calls <- md:
				default:
				}
				return status.Error(codes.PermissionDenied, "fake frontend")
			},
		),
	}
	if useTLS {
		var serverTLS *tls.Config
		serverTLS, f.clientTLS = selfSignedTLS(t)
		serverOpts = append(serverOpts, grpc.Creds(credentials.NewTLS(serverTLS)))
	}

	server := grpc.NewServer(serverOpts...)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	return f
}

func (f *fakeFrontend) call(t *testing.T, options client.Options) metadata.MD {
	c, err := client.NewLazyClient(options)
	require.NoError(t, err)
	t.Cleanup(c.Close)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, _ = c.DescribeWorkflowExecution(ctx, "workflow", "")

	select {
	case md := <-f.calls:
		return md
	default:
		require.FailNow(t, "the call didn't reach the frontend")
		return nil
	}
}

func selfSignedTLS(t *testing.T) (server, client *tls.Config) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)

	roots := x509.NewCertPool()
	roots.AddCert(cert)
	server = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	client = &tls.Config{RootCAs: roots}
	return server, client
}
