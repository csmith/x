package smtp

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"math/big"
	"net"
	"net/smtp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const message = "Subject: test\r\n\r\nHello\r\n"

func TestSendMail_deliversMessage(t *testing.T) {
	addr, sessions, _ := fakeServer{}.start(t)

	err := SendMail(
		t.Context(),
		addr,
		nil,
		"a@example.com",
		[]string{"b@example.com", "c@example.com"},
		[]byte(message),
	)

	require.NoError(t, err)
	session := <-sessions
	assert.Equal(
		t,
		[]string{
			"EHLO localhost",
			"MAIL FROM:<a@example.com>",
			"RCPT TO:<b@example.com>",
			"RCPT TO:<c@example.com>",
			"DATA",
			"QUIT",
		},
		session.commands,
	)
	assert.Equal(t, message, session.data)
	assert.False(t, session.tls)
}

func TestSendMail_usesStartTLSWhenOffered(t *testing.T) {
	serverConfig, clientConfig := testTLSConfigs(t)
	addr, sessions, _ := fakeServer{starttls: serverConfig}.start(t)

	err := sendMail(
		t.Context(),
		addr,
		clientConfig,
		nil,
		"a@example.com",
		[]string{"b@example.com"},
		[]byte(message),
	)

	require.NoError(t, err)
	session := <-sessions
	assert.Equal(t, []string{"EHLO localhost", "STARTTLS", "EHLO localhost"}, session.commands[:3])
	assert.True(t, session.tls)
	assert.Equal(t, message, session.data)
}

func TestSendMail_failsIfStartTLSCertificateIsUntrusted(t *testing.T) {
	serverConfig, _ := testTLSConfigs(t)
	addr, sessions, _ := fakeServer{starttls: serverConfig}.start(t)

	err := SendMail(t.Context(), addr, nil, "a@example.com", []string{"b@example.com"}, []byte(message))

	require.Error(t, err)
	assert.Empty(t, (<-sessions).data)
}

func TestSendMail_authenticates(t *testing.T) {
	addr, sessions, _ := fakeServer{auth: true}.start(t)

	err := SendMail(
		t.Context(),
		addr,
		smtp.PlainAuth("", "user", "pass", "127.0.0.1"),
		"a@example.com",
		[]string{"b@example.com"},
		[]byte(message),
	)

	require.NoError(t, err)
	session := <-sessions
	assert.Equal(
		t,
		[]string{
			"EHLO localhost",
			"AUTH PLAIN AHVzZXIAcGFzcw==",
			"MAIL FROM:<a@example.com>",
		},
		session.commands[:3],
	)
	assert.Equal(t, message, session.data)
}

func TestSendMail_authenticatesAfterStartTLS(t *testing.T) {
	serverConfig, clientConfig := testTLSConfigs(t)
	addr, sessions, _ := fakeServer{starttls: serverConfig, auth: true}.start(t)

	err := sendMail(
		t.Context(),
		addr,
		clientConfig,
		smtp.PlainAuth("", "user", "pass", "127.0.0.1"),
		"a@example.com",
		[]string{"b@example.com"},
		[]byte(message),
	)

	require.NoError(t, err)
	session := <-sessions
	assert.Equal(
		t,
		[]string{
			"EHLO localhost",
			"STARTTLS",
			"EHLO localhost",
			"AUTH PLAIN AHVzZXIAcGFzcw==",
		},
		session.commands[:4],
	)
	assert.True(t, session.tls)
}

func TestSendMail_skipsAuthWithoutCredentials(t *testing.T) {
	addr, sessions, _ := fakeServer{auth: true}.start(t)

	err := SendMail(t.Context(), addr, nil, "a@example.com", []string{"b@example.com"}, []byte(message))

	require.NoError(t, err)
	assert.Equal(t, "MAIL FROM:<a@example.com>", (<-sessions).commands[1])
}

func TestSendMail_failsIfAuthNotOffered(t *testing.T) {
	addr, sessions, _ := fakeServer{}.start(t)

	err := SendMail(
		t.Context(),
		addr,
		smtp.PlainAuth("", "user", "pass", "127.0.0.1"),
		"a@example.com",
		[]string{"b@example.com"},
		[]byte(message),
	)

	require.Error(t, err)
	assert.NotContains(t, (<-sessions).commands, "MAIL FROM:<a@example.com>")
}

func TestSendMail_returnsRejection(t *testing.T) {
	addr, _, _ := fakeServer{replies: map[string]string{".": "554 no thanks"}}.start(t)

	err := SendMail(t.Context(), addr, nil, "a@example.com", []string{"b@example.com"}, []byte(message))

	require.ErrorContains(t, err, "no thanks")
}

func TestSendMail_closesConnectionWhenCancelledMidSend(t *testing.T) {
	addr, sessions, stalled := fakeServer{replies: map[string]string{".": stall}}.start(t)
	ctx, cancel := context.WithCancel(t.Context())

	result := sendAsync(ctx, addr)
	<-stalled
	cancel()

	require.ErrorIs(t, awaitResult(t, result), context.Canceled)
	select {
	case session := <-sessions:
		assert.Equal(t, message, session.data)
	case <-time.After(5 * time.Second):
		require.Fail(t, "connection wasn't closed")
	}
}

func TestSendMail_returnsCauseWhenCancelledBeforeDial(t *testing.T) {
	addr, _, _ := fakeServer{}.start(t)
	cause := errors.New("shutting down")
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(cause)

	require.ErrorIs(t, SendMail(ctx, addr, nil, "from@example.com", []string{"to@example.com"}, []byte(message)), cause)
}

func TestSendMail_stopsAtDeadline(t *testing.T) {
	addr, _, _ := fakeServer{replies: map[string]string{"MAIL": stall}}.start(t)
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	result := sendAsync(ctx, addr)

	require.ErrorIs(t, awaitResult(t, result), context.DeadlineExceeded)
}

func TestSendMail_succeedsIfQuitFails(t *testing.T) {
	addr, sessions, _ := fakeServer{replies: map[string]string{"QUIT": hangUp}}.start(t)

	err := SendMail(t.Context(), addr, nil, "a@example.com", []string{"b@example.com"}, []byte(message))

	require.NoError(t, err)
	assert.Equal(t, message, (<-sessions).data)
}

func TestSendMail_succeedsIfCancelledDuringQuit(t *testing.T) {
	addr, sessions, stalled := fakeServer{replies: map[string]string{"QUIT": stall}}.start(t)
	ctx, cancel := context.WithCancel(t.Context())

	result := sendAsync(ctx, addr)
	<-stalled
	cancel()

	require.NoError(t, awaitResult(t, result))
	assert.Equal(t, message, (<-sessions).data)
}

func sendAsync(ctx context.Context, addr string) <-chan error {
	result := make(chan error, 1)
	go func() {
		result <- SendMail(ctx, addr, nil, "a@example.com", []string{"b@example.com"}, []byte(message))
	}()
	return result
}

func awaitResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		require.Fail(t, "SendMail didn't return")
		return nil
	}
}

const (
	// stall makes the fake server stop replying, and wait for the client to
	// hang up.
	stall = "stall"
	// hangUp makes the fake server close the connection without replying.
	hangUp = "hang up"
)

type fakeServer struct {
	// starttls, if set, makes the server offer STARTTLS using this config.
	starttls *tls.Config
	auth     bool
	// replies overrides the reply to commands, keyed by verb, or "." for the
	// end of the message data.
	replies map[string]string
}

type session struct {
	commands []string
	data     string
	tls      bool
}

// start accepts a single connection, and sends what it received on sessions
// once the connection is closed. stalled is closed if the server stalls.
func (f fakeServer) start(t *testing.T) (addr string, sessions <-chan session, stalled <-chan struct{}) {
	t.Helper()

	listener, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	sessionCh := make(chan session, 1)
	stalledCh := make(chan struct{})
	go func() {
		c := &fakeConn{server: f, stalled: stalledCh}
		defer func() { sessionCh <- c.session }()

		conn, err := listener.Accept()
		if err != nil {
			return
		}
		c.setConn(conn)
		defer func() { _ = c.conn.Close() }()
		c.serve()
	}()

	return listener.Addr().String(), sessionCh, stalledCh
}

type fakeConn struct {
	server  fakeServer
	stalled chan struct{}
	conn    net.Conn
	reader  *bufio.Reader
	session session
}

func (c *fakeConn) serve() {
	if !c.reply("greeting", "220 test") {
		return
	}
	for {
		line, err := c.reader.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimSuffix(line, "\r\n")
		c.session.commands = append(c.session.commands, line)
		verb, _, _ := strings.Cut(line, " ")
		if !c.handle(verb) {
			return
		}
	}
}

// handle responds to a command, and returns whether the connection should
// stay open.
func (c *fakeConn) handle(verb string) bool {
	switch verb {
	case "EHLO":
		return c.reply(verb, c.ehloReply())
	case "STARTTLS":
		if !c.reply(verb, "220 go ahead") {
			return false
		}
		c.setConn(tls.Server(c.conn, c.server.starttls))
		c.session.tls = true
		return true
	case "AUTH":
		return c.reply(verb, "235 ok")
	case "DATA":
		return c.reply(verb, "354 go ahead") && c.readData() && c.reply(".", "250 ok")
	case "QUIT":
		c.reply(verb, "221 bye")
		return false
	default:
		return c.reply(verb, "250 ok")
	}
}

func (c *fakeConn) ehloReply() string {
	lines := []string{"test"}
	if c.server.starttls != nil && !c.session.tls {
		lines = append(lines, "STARTTLS")
	}
	if c.server.auth {
		lines = append(lines, "AUTH PLAIN")
	}
	for i := range lines {
		separator := "-"
		if i == len(lines)-1 {
			separator = " "
		}
		lines[i] = "250" + separator + lines[i]
	}
	return strings.Join(lines, "\r\n")
}

func (c *fakeConn) readData() bool {
	var data strings.Builder
	defer func() { c.session.data = data.String() }()
	for {
		line, err := c.reader.ReadString('\n')
		if err != nil {
			return false
		}
		if line == ".\r\n" {
			return true
		}
		data.WriteString(line)
	}
}

// reply sends the reply configured for key, or fallback if there isn't one,
// and returns whether the connection should stay open.
func (c *fakeConn) reply(key, fallback string) bool {
	response := fallback
	if override, ok := c.server.replies[key]; ok {
		response = override
	}
	switch response {
	case stall:
		close(c.stalled)
		_, _ = c.reader.WriteTo(io.Discard)
		return false
	case hangUp:
		return false
	default:
		_, err := c.conn.Write([]byte(response + "\r\n"))
		return err == nil
	}
}

func (c *fakeConn) setConn(conn net.Conn) {
	c.conn = conn
	c.reader = bufio.NewReader(conn)
}

func testTLSConfigs(t *testing.T) (server, client *tls.Config) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)

	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}},
		&tls.Config{RootCAs: pool, ServerName: "127.0.0.1"}
}
