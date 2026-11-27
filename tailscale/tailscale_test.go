package tailscale

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tsnet"
)

var (
	_ Upper    = (*tsnet.Server)(nil)
	_ Listener = (*tsnet.Server)(nil)
)

type fakeUpper struct {
	status *ipnstate.Status
	err    error
}

func (f fakeUpper) Up(context.Context) (*ipnstate.Status, error) {
	return f.status, f.err
}

func TestUp_returnsNameWithoutTrailingDot(t *testing.T) {
	name, err := Up(
		t.Context(),
		fakeUpper{
			status: &ipnstate.Status{
				Self: &ipnstate.PeerStatus{DNSName: "node.example.ts.net."},
			},
		},
	)
	require.NoError(t, err)
	assert.Equal(t, "node.example.ts.net", name)
}

func TestUp_failsWithoutName(t *testing.T) {
	_, err := Up(t.Context(), fakeUpper{
		status: &ipnstate.Status{},
	})
	require.Error(t, err)

	_, err = Up(t.Context(), fakeUpper{
		status: &ipnstate.Status{
			Self: &ipnstate.PeerStatus{},
		},
	})
	require.Error(t, err)
}

func TestUp_wrapsError(t *testing.T) {
	failure := errors.New("no login")
	_, err := Up(t.Context(), fakeUpper{err: failure})
	assert.ErrorIs(t, err, failure)
}

// fakeTailnet listens on loopback in place of the tailnet, without TLS.
type fakeTailnet struct {
	http, https  net.Listener
	listenErr    error
	listenTLSErr error
}

func newFakeTailnet(t *testing.T) *fakeTailnet {
	listen := func() net.Listener {
		l, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
		require.NoError(t, err)
		t.Cleanup(func() { _ = l.Close() })
		return l
	}
	return &fakeTailnet{http: listen(), https: listen()}
}

func (f *fakeTailnet) Listen(network, addr string) (net.Listener, error) {
	if network != "tcp" || addr != ":80" {
		return nil, errors.New("unexpected listen address")
	}
	return f.http, f.listenErr
}

func (f *fakeTailnet) ListenTLS(network, addr string) (net.Listener, error) {
	if network != "tcp" || addr != ":443" {
		return nil, errors.New("unexpected TLS listen address")
	}
	return f.https, f.listenTLSErr
}

func startServing(t *testing.T, tailnet *fakeTailnet, handler http.Handler) (context.CancelFunc, <-chan error) {
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, tailnet, "node.example.ts.net", handler) }()
	t.Cleanup(cancel)
	return cancel, done
}

var noRedirects = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func get(ctx context.Context, url, host string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if host != "" {
		req.Host = host
	}
	return noRedirects.Do(req)
}

func TestServe_redirectsPlainHTTPToFullName(t *testing.T) {
	tailnet := newFakeTailnet(t)
	startServing(t, tailnet, http.NotFoundHandler())

	res, err := get(t.Context(), "http://"+tailnet.http.Addr().String()+"/some/path%2Fhere?a=1&b=2", "node")
	require.NoError(t, err)
	_ = res.Body.Close()

	assert.Equal(t, http.StatusMovedPermanently, res.StatusCode)
	assert.Equal(t, "https://node.example.ts.net/some/path%2Fhere?a=1&b=2", res.Header.Get("Location"))
}

func TestServe_servesHandlerOnHTTPSListener(t *testing.T) {
	tailnet := newFakeTailnet(t)
	startServing(
		t,
		tailnet,
		http.HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, "hello "+r.URL.Path)
			},
		),
	)

	res, err := get(t.Context(), "http://"+tailnet.https.Addr().String()+"/world", "")
	require.NoError(t, err)
	body, err := io.ReadAll(res.Body)
	_ = res.Body.Close()
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, res.StatusCode)
	assert.Equal(t, "hello /world", string(body))
}

func TestServe_finishesInFlightRequestsOnShutdown(t *testing.T) {
	tailnet := newFakeTailnet(t)
	started := make(chan struct{})
	release := make(chan struct{})
	cancel, done := startServing(
		t,
		tailnet,
		http.HandlerFunc(
			func(w http.ResponseWriter, _ *http.Request) {
				close(started)
				<-release
				_, _ = io.WriteString(w, "finished")
			},
		),
	)

	type result struct {
		body string
		err  error
	}
	responses := make(chan result, 1)
	go func() {
		res, err := get(t.Context(), "http://"+tailnet.https.Addr().String()+"/", "")
		if err != nil {
			responses <- result{err: err}
			return
		}
		body, err := io.ReadAll(res.Body)
		_ = res.Body.Close()
		responses <- result{string(body), err}
	}()

	<-started
	cancel()
	select {
	case err := <-done:
		t.Fatalf("Serve returned before the request finished: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)

	response := <-responses
	require.NoError(t, response.err)
	assert.Equal(t, "finished", response.body)
	assert.NoError(t, <-done)
}

func TestServe_returnsListenErrors(t *testing.T) {
	failure := errors.New("listen failed")

	tailnet := newFakeTailnet(t)
	tailnet.listenErr = failure
	require.ErrorIs(t, Serve(t.Context(), tailnet, "node.example.ts.net", http.NotFoundHandler()), failure)

	tailnet = newFakeTailnet(t)
	tailnet.listenTLSErr = failure
	require.ErrorIs(t, Serve(t.Context(), tailnet, "node.example.ts.net", http.NotFoundHandler()), failure)
}

func TestServe_returnsServeErrors(t *testing.T) {
	tailnet := newFakeTailnet(t)
	require.NoError(t, tailnet.https.Close())

	err := Serve(t.Context(), tailnet, "node.example.ts.net", http.NotFoundHandler())
	assert.ErrorIs(t, err, net.ErrClosed)
}

func newTestLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(
		slog.NewTextHandler(
			buf,
			&slog.HandlerOptions{
				Level: slog.LevelDebug,
				ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
					if a.Key == slog.TimeKey {
						return slog.Attr{}
					}
					return a
				},
			},
		),
	)
}

func TestNewServer_routesLogsToDefaultLogger(t *testing.T) {
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(newTestLogger(&buf))
	t.Cleanup(func() { slog.SetDefault(previous) })

	s := NewServer("node", "/state", "key")
	assert.Equal(t, "node", s.Hostname)
	assert.Equal(t, "/state", s.Dir)
	assert.Equal(t, "key", s.AuthKey)

	s.UserLogf("visit %s to log in\n", "https://login.example")
	s.Logf("backend %d", 42)

	assert.Equal(
		t,
		"level=INFO msg=\"visit https://login.example to log in\" component=tailscale\n"+
			"level=DEBUG msg=\"backend 42\" component=tailscale\n",
		buf.String(),
	)
}

func TestNewServer_routesLogsToGivenLogger(t *testing.T) {
	var defaultBuf, buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(newTestLogger(&defaultBuf))
	t.Cleanup(func() { slog.SetDefault(previous) })

	s := NewServer("node", "", "", WithLogger(newTestLogger(&buf).With("node", "a")))
	s.UserLogf("visit %s to log in\n", "https://login.example")
	s.Logf("backend %d", 42)

	assert.Equal(
		t,
		"level=INFO msg=\"visit https://login.example to log in\" node=a\n"+
			"level=DEBUG msg=\"backend 42\" node=a\n",
		buf.String(),
	)
	assert.Empty(t, defaultBuf.String())
}

func TestNewServer_skipsDisabledLevels(t *testing.T) {
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	NewServer("node", "", "").Logf("backend chatter")

	assert.Empty(t, buf.String())
}
