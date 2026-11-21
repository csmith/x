// Package smtp sends mail over SMTP with the whole exchange bounded by a
// context.
package smtp

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/smtp"
)

// SendMail sends msg from from to each address in to via the SMTP server at
// addr, which must include a port. It's the equivalent of [smtp.SendMail],
// except the whole exchange is bounded by ctx: if ctx is done before the
// message is sent, the connection is closed and ctx's error is returned.
// ctx should normally carry a deadline, as nothing else stops a server that
// stops responding.
//
// The connection is upgraded with STARTTLS if the server offers it,
// verifying the server's certificate against addr's host. If auth is
// non-nil the server must offer AUTH, and auth is used to authenticate;
// if it's nil, no authentication is attempted.
//
// Once the server has accepted the message, it counts as sent: if the
// QUIT that follows fails, or ctx is done before it completes, the failure
// is logged and SendMail returns nil so that a retry doesn't deliver the
// message again.
func SendMail(ctx context.Context, addr string, auth smtp.Auth, from string, to []string, msg []byte) error {
	return sendMail(ctx, addr, nil, auth, from, to, msg)
}

func sendMail(
	ctx context.Context,
	addr string,
	tlsConfig *tls.Config,
	auth smtp.Auth,
	from string,
	to []string,
	msg []byte,
) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("parse address: %w", err)
	}
	if tlsConfig == nil {
		tlsConfig = &tls.Config{ServerName: host}
	}

	conn, err := new(net.Dialer).DialContext(ctx, "tcp", addr)
	if err != nil {
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		return fmt.Errorf("dial: %w", err)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	if err := deliver(conn, host, tlsConfig, auth, from, to, msg); err != nil {
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		return err
	}
	return nil
}

func deliver(
	conn net.Conn,
	host string,
	tlsConfig *tls.Config,
	auth smtp.Auth,
	from string,
	to []string,
	msg []byte,
) error {
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("greeting: %w", err)
	}
	defer func() { _ = c.Close() }()

	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(tlsConfig); err != nil {
			return fmt.Errorf("starttls: %w", err)
		}
	}
	if auth != nil {
		if ok, _ := c.Extension("AUTH"); !ok {
			return errors.New("auth: server doesn't support AUTH")
		}
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("auth: %w", err)
		}
	}
	if err := c.Mail(from); err != nil {
		return fmt.Errorf("mail: %w", err)
	}
	for _, recipient := range to {
		if err := c.Rcpt(recipient); err != nil {
			return fmt.Errorf("rcpt: %w", err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("data: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("data: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("data: %w", err)
	}

	if err := c.Quit(); err != nil {
		slog.Warn("SMTP server accepted the message but QUIT failed", "host", host, "error", err)
	}
	return nil
}
