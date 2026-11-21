package http

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func listen(t *testing.T) net.Listener {
	t.Helper()
	l, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })
	return l
}

func get(ctx context.Context, url, host string) (int, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, "", err
	}
	if host != "" {
		req.Host = host
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(res.Body)
	return res.StatusCode, string(body), err
}

type response struct {
	status int
	body   string
	err    error
}

func getAsync(ctx context.Context, url string) <-chan response {
	responses := make(chan response, 1)
	go func() {
		status, body, err := get(ctx, url, "")
		responses <- response{status, body, err}
	}()
	return responses
}

// blockingHandler signals started when a request arrives, then waits for
// release before responding.
func blockingHandler(started chan<- struct{}, release <-chan struct{}) http.Handler {
	return http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			close(started)
			<-release
			_, _ = io.WriteString(w, "finished")
		},
	)
}

func TestServe_servesUntilCancelled(t *testing.T) {
	l := listen(t)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	server := &http.Server{
		Handler: http.HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, "hello "+r.URL.Path)
			},
		),
	}
	go func() { done <- Serve(ctx, server, l) }()

	status, body, err := get(t.Context(), "http://"+l.Addr().String()+"/world", "")
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, "hello /world", body)

	cancel()
	require.NoError(t, <-done)
	_, _, err = get(t.Context(), "http://"+l.Addr().String()+"/", "")
	assert.Error(t, err)
}

func TestServe_finishesInFlightRequestsOnShutdown(t *testing.T) {
	l := listen(t)
	started := make(chan struct{})
	release := make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, &http.Server{Handler: blockingHandler(started, release)}, l) }()

	responses := getAsync(t.Context(), "http://"+l.Addr().String()+"/")
	<-started
	cancel()
	select {
	case err := <-done:
		t.Fatalf("Serve returned before the request finished: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)

	res := <-responses
	require.NoError(t, res.err)
	assert.Equal(t, "finished", res.body)
	assert.NoError(t, <-done)
}

func TestServe_closesConnectionsAfterShutdownTimeout(t *testing.T) {
	previous := shutdownTimeout
	shutdownTimeout = 50 * time.Millisecond
	t.Cleanup(func() { shutdownTimeout = previous })

	l := listen(t)
	started := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, &http.Server{Handler: blockingHandler(started, release)}, l) }()

	responses := getAsync(t.Context(), "http://"+l.Addr().String()+"/")
	<-started
	cancel()

	require.ErrorIs(t, <-done, context.DeadlineExceeded)
	assert.Error(t, (<-responses).err)
}

func TestServe_returnsServeErrors(t *testing.T) {
	l := listen(t)
	require.NoError(t, l.Close())

	assert.ErrorIs(t, Serve(t.Context(), &http.Server{}, l), net.ErrClosed)
}

func TestServe_setsDefaultTimeouts(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	server := &http.Server{}
	require.NoError(t, Serve(ctx, server, listen(t)))

	assert.Equal(t, defaultReadHeaderTimeout, server.ReadHeaderTimeout)
	assert.Equal(t, defaultIdleTimeout, server.IdleTimeout)
}

func TestServe_keepsCallerTimeouts(t *testing.T) {
	type timeouts struct {
		read, readHeader, idle time.Duration
	}
	tests := []struct {
		name         string
		server, want timeouts
	}{
		{
			name:   "read header timeout",
			server: timeouts{readHeader: time.Second},
			want:   timeouts{readHeader: time.Second, idle: defaultIdleTimeout},
		},
		{
			name:   "idle timeout",
			server: timeouts{idle: -1},
			want:   timeouts{readHeader: defaultReadHeaderTimeout, idle: -1},
		},
		{
			name:   "read timeout",
			server: timeouts{read: time.Second},
			want:   timeouts{read: time.Second},
		},
	}
	for _, tt := range tests {
		t.Run(
			tt.name,
			func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				server := &http.Server{
					ReadTimeout:       tt.server.read,
					ReadHeaderTimeout: tt.server.readHeader,
					IdleTimeout:       tt.server.idle,
				}
				require.NoError(t, Serve(ctx, server, listen(t)))

				assert.Equal(
					t,
					tt.want,
					timeouts{server.ReadTimeout, server.ReadHeaderTimeout, server.IdleTimeout},
				)
			},
		)
	}
}

func TestListenAndServe_returnsListenErrors(t *testing.T) {
	l := listen(t)

	err := ListenAndServe(t.Context(), &http.Server{Addr: l.Addr().String()})
	assert.ErrorContains(t, err, "listen")
}

func TestListenAndServe_servesOnAddr(t *testing.T) {
	l := listen(t)
	address := l.Addr().String()
	require.NoError(t, l.Close())

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- ListenAndServe(ctx, &http.Server{Addr: address, Handler: http.NotFoundHandler()})
	}()

	require.EventuallyWithT(
		t,
		func(c *assert.CollectT) {
			status, _, err := get(t.Context(), "http://"+address+"/", "")
			assert.NoError(c, err)
			assert.Equal(c, http.StatusNotFound, status)
		},
		5*time.Second,
		10*time.Millisecond,
	)
	cancel()
	assert.NoError(t, <-done)
}

func TestServeLocal_returnsListenErrors(t *testing.T) {
	l := listen(t)

	assert.ErrorContains(t, ServeLocal(t.Context(), l.Addr().String(), http.NotFoundHandler()), "listen")
}

func TestServeLocal_checksHostOnListeningPort(t *testing.T) {
	l := listen(t)
	address := l.Addr().String()
	_, port, err := net.SplitHostPort(address)
	require.NoError(t, err)
	require.NoError(t, l.Close())

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- ServeLocal(ctx, "127.0.0.1:"+port, http.NotFoundHandler()) }()

	require.EventuallyWithT(
		t,
		func(c *assert.CollectT) {
			status, _, err := get(t.Context(), "http://"+address+"/", "LOCALHOST:"+port)
			assert.NoError(c, err)
			assert.Equal(c, http.StatusNotFound, status)
		},
		5*time.Second,
		10*time.Millisecond,
	)

	status, _, err := get(t.Context(), "http://"+address+"/", "attacker.test:"+port)
	require.NoError(t, err)
	assert.Equal(t, http.StatusMisdirectedRequest, status)

	cancel()
	assert.NoError(t, <-done)
}

func TestServeLocalListener_checksHostOnBoundPort(t *testing.T) {
	l := listen(t)
	address := l.Addr().String()
	_, port, err := net.SplitHostPort(address)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- ServeLocalListener(ctx, l, http.NotFoundHandler()) }()

	for _, host := range []string{address, "localhost:" + port, "LOCALHOST:" + port, "[::1]:" + port} {
		status, _, err := get(t.Context(), "http://"+address+"/", host)
		require.NoError(t, err)
		assert.Equal(t, http.StatusNotFound, status, host)
	}
	for _, host := range []string{"attacker.test:" + port, "localhost:0", "localhost"} {
		status, _, err := get(t.Context(), "http://"+address+"/", host)
		require.NoError(t, err)
		assert.Equal(t, http.StatusMisdirectedRequest, status, host)
	}

	cancel()
	assert.NoError(t, <-done)
}

func TestCheckHost(t *testing.T) {
	tests := []struct {
		host, port, requestHost string
		accepted                bool
	}{
		{"127.0.0.1", "3458", "127.0.0.1:3458", true},
		{"127.0.0.1", "3458", "localhost:3458", true},
		{"127.0.0.1", "3458", "[::1]:3458", true},
		{"127.0.0.1", "3458", "LocalHost:3458", true},
		{"service.test", "3458", "SERVICE.test:3458", true},
		{"::1", "3458", "[::1]:3458", true},
		{"", "3458", "localhost:3458", true},
		{"0.0.0.0", "3458", "127.0.0.1:3458", true},
		{"127.0.0.1", "3458", "attacker.test:3458", false},
		{"127.0.0.1", "3458", "localhost:9999", false},
		{"127.0.0.1", "3458", "127.0.0.1", false},
		{"", "3458", ":3458", false},
		{"127.0.0.1", "80", "localhost", true},
		{"127.0.0.1", "80", "127.0.0.1", true},
		{"127.0.0.1", "80", "localhost:80", true},
		{"::1", "80", "[::1]", true},
		{"service.test", "80", "Service.Test", true},
		{"127.0.0.1", "80", "attacker.test", false},
		{"127.0.0.1", "80", "localhost:8080", false},
	}
	for _, tt := range tests {
		t.Run(
			tt.host+" "+tt.port+" "+tt.requestHost,
			func(t *testing.T) {
				req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
				req.Host = tt.requestHost
				res := httptest.NewRecorder()
				checkHost(tt.host, tt.port, http.NotFoundHandler()).ServeHTTP(res, req)
				if tt.accepted {
					assert.Equal(t, http.StatusNotFound, res.Code)
				} else {
					assert.Equal(t, http.StatusMisdirectedRequest, res.Code)
				}
			},
		)
	}
}
