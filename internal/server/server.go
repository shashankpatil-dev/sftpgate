// Package server accepts SSH connections, checks who is connecting and
// starts an SFTP session for them. Nothing but SFTP is allowed: no shell,
// no commands, no port forwarding.
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/sftp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/crypto/ssh"

	"github.com/shashankpatil-dev/sftpgate/internal/audit"
	"github.com/shashankpatil-dev/sftpgate/internal/config"
	"github.com/shashankpatil-dev/sftpgate/internal/metrics"
	"github.com/shashankpatil-dev/sftpgate/internal/storage"
	"github.com/shashankpatil-dev/sftpgate/internal/tracing"
	"github.com/shashankpatil-dev/sftpgate/internal/vfs"
)

// handshakeTimeout is how long a client has to finish logging in.
const handshakeTimeout = 30 * time.Second

// Server is the SFTP server.
type Server struct {
	// Config returns the current config. Each connection keeps the one it
	// started with until it ends.
	Config func() *config.Config

	Buckets  *storage.Registry
	Throttle *Throttle

	// AfterUpload receives every file saved into a folder with on_upload
	// steps. It may be nil.
	AfterUpload func(vfs.Uploaded) error

	mu       sync.Mutex
	sessions map[string]*session
	wg       sync.WaitGroup
}

// session is one logged-in SSH connection.
type session struct {
	id      string
	account *config.Account
	conn    *ssh.ServerConn
}

// New returns a server that reads its config from cfg.
func New(cfg func() *config.Config, buckets *storage.Registry) *Server {
	return &Server{
		Config:   cfg,
		Buckets:  buckets,
		Throttle: NewThrottle(),
		sessions: map[string]*session{},
	}
}

// Serve accepts connections on ln until ctx is cancelled.
// It then waits for open sessions to finish, as long as grace allows.
func (s *Server) Serve(ctx context.Context, ln net.Listener, grace time.Duration) error {
	go s.sweepThrottle(ctx)
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				s.shutdown(grace)
				return nil
			}
			return err
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handle(c)
		}()
	}
}

// shutdown waits for sessions to end on their own, so uploads in progress can
// finish, then closes whatever is still open.
func (s *Server) shutdown(grace time.Duration) {
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(grace):
		s.CloseSessions(func(*config.Account) bool { return true }, "server shutting down")
		<-done
	}
}

// CloseSessions closes every session whose account matches. Uploads that
// are cut off are dropped, never saved half-way.
func (s *Server) CloseSessions(match func(*config.Account) bool, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, sess := range s.sessions {
		if match(sess.account) {
			slog.Info("closing session", "session", sess.id, "account", sess.account.Name, "reason", reason)
			_ = sess.conn.Close()
		}
	}
}

// CloseChanged closes the sessions of accounts that were removed or changed
// in newCfg. Everyone else keeps working.
func (s *Server) CloseChanged(newCfg *config.Config) {
	s.CloseSessions(func(old *config.Account) bool {
		now := newCfg.Account(old.Name)
		return now == nil || now.Fingerprint != old.Fingerprint
	}, "account changed in config")
}

// handle runs one connection from the first byte to the end.
func (s *Server) handle(c net.Conn) {
	defer c.Close()

	cfg := s.Config()
	ip := remoteIP(c)
	ev := audit.Event{IP: ip.String(), Config: cfg.Hash}

	// Cheap checks first, before spending any CPU on SSH crypto.
	if !config.AllowsIP(cfg.GlobalIPs, ip) {
		s.reject(ev, "rejected_ip", "ip not allowed")
		return
	}
	if s.Throttle.Blocked(ip) {
		s.reject(ev, "rejected_throttle", "too many failed logins")
		return
	}
	metrics.Connections.WithLabelValues("accepted").Inc()

	conn := newIdleConn(c, handshakeTimeout)
	auth := &authenticator{cfg: cfg, ip: ip}
	sshConn, chans, reqs, err := ssh.NewServerConn(conn, auth.sshConfig())
	if err != nil {
		ev.User = auth.user
		s.loginFailed(ev, cfg, ip, err)
		return
	}
	defer sshConn.Close()
	conn.setTimeout(cfg.Server.IdleTimeout)

	sess := &session{
		id:      newSessionID(),
		account: cfg.Account(sshConn.Permissions.Extensions[extAccount]),
		conn:    sshConn,
	}
	ev.Session, ev.Account = sess.id, sess.account.Name
	ev.Method = sshConn.Permissions.Extensions[extMethod]
	ev.KeyFP = sshConn.Permissions.Extensions[extKeyFP]
	metrics.Auth.WithLabelValues(sess.account.Name, ev.Method, audit.OK).Inc()

	if err := s.addSession(sess); err != nil {
		ev.Event, ev.Result, ev.Error = "auth_ok", audit.Denied, err.Error()
		audit.Emit(ev)
		return
	}
	defer s.removeSession(sess)

	ctx, span := tracing.Tracer.Start(context.Background(), "sftp.session", trace.WithAttributes(
		attribute.String("sftpgate.account", sess.account.Name),
		attribute.String("sftpgate.session", sess.id),
		attribute.String("sftpgate.auth_method", ev.Method),
		attribute.String("client.address", ip.String()),
	))
	defer span.End()
	ev.TraceID = tracing.TraceID(ctx)

	ev.Event, ev.Result = "session_start", audit.OK
	audit.Emit(ev)
	start := time.Now()

	go ssh.DiscardRequests(reqs)
	s.serveChannels(chans, &vfs.Handler{
		Config:      cfg,
		Account:     sess.account,
		Buckets:     s.Buckets,
		SessionID:   sess.id,
		IP:          ip.String(),
		AfterUpload: s.AfterUpload,
		Ctx:         ctx,
	})

	ev.Event, ev.Method, ev.KeyFP = "session_end", "", ""
	ev.DurationMS = time.Since(start).Milliseconds()
	audit.Emit(ev)
}

// serveChannels answers channel requests until the connection closes.
// Only "session" channels running the "sftp" subsystem are allowed.
func (s *Server) serveChannels(chans <-chan ssh.NewChannel, h *vfs.Handler) {
	var wg sync.WaitGroup
	for nc := range chans {
		if nc.ChannelType() != "session" {
			_ = nc.Reject(ssh.UnknownChannelType, "only sftp is allowed")
			continue
		}
		ch, reqs, err := nc.Accept()
		if err != nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer ch.Close()
			serveSessionChannel(ch, reqs, h)
		}()
	}
	wg.Wait()
}

// serveSessionChannel waits for the client to ask for SFTP and runs it.
// Every other request (shell, exec, scp, pty, x11, agent) is refused.
func serveSessionChannel(ch ssh.Channel, reqs <-chan *ssh.Request, h *vfs.Handler) {
	for req := range reqs {
		switch {
		case req.Type == "subsystem" && subsystemName(req.Payload) == "sftp":
			_ = req.Reply(true, nil)
			go ssh.DiscardRequests(reqs)

			srv := sftp.NewRequestServer(ch, h.Handlers())
			if err := srv.Serve(); err != nil && !errors.Is(err, io.EOF) {
				slog.Debug("sftp session ended", "session", h.SessionID, "err", err)
			}
			_ = srv.Close()
			return
		case req.Type == "env":
			// Clients often send LANG and similar. Harmless, so say yes and ignore it.
			_ = req.Reply(true, nil)
		default:
			_ = req.Reply(false, nil)
		}
	}
}

// subsystemName reads the name from a "subsystem" request.
func subsystemName(payload []byte) string {
	var msg struct{ Name string }
	if err := ssh.Unmarshal(payload, &msg); err != nil {
		return ""
	}
	return msg.Name
}

func (s *Server) reject(ev audit.Event, result, reason string) {
	metrics.Connections.WithLabelValues(result).Inc()
	ev.Event, ev.Result, ev.Error = "conn_rejected", audit.Denied, reason
	audit.Emit(ev)
}

// loginFailed records a connection that ended without logging in.
//
// It counts as one failure for the throttle, no matter how many keys the
// client tried. Trying several keys is normal for OpenSSH clients.
func (s *Server) loginFailed(ev audit.Event, cfg *config.Config, ip netip.Addr, err error) {
	var authErr *ssh.ServerAuthError
	if !errors.As(err, &authErr) {
		// The client went away or spoke bad SSH before trying to log in.
		slog.Debug("handshake failed", "ip", ip, "err", err)
		return
	}

	s.Throttle.Fail(ip, cfg.Server.Throttle)
	metrics.Auth.WithLabelValues(metrics.UnknownAccount, "any", audit.Failed).Inc()
	ev.Event, ev.Result, ev.Error = "auth_failed", audit.Failed, "authentication failed"
	audit.Emit(ev)
}

func (s *Server) addSession(sess *session) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if limit := sess.account.MaxSessions; limit > 0 {
		open := 0
		for _, other := range s.sessions {
			if other.account.Name == sess.account.Name {
				open++
			}
		}
		if open >= limit {
			return fmt.Errorf("too many sessions (max_sessions is %d)", limit)
		}
	}

	s.sessions[sess.id] = sess
	metrics.SessionsActive.WithLabelValues(sess.account.Name).Inc()
	return nil
}

func (s *Server) removeSession(sess *session) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.sessions, sess.id)
	metrics.SessionsActive.WithLabelValues(sess.account.Name).Dec()
}

// sweepThrottle cleans up the throttle once a minute.
func (s *Server) sweepThrottle(ctx context.Context) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			s.Throttle.Sweep(s.Config().Server.Throttle)
		}
	}
}

func remoteIP(c net.Conn) netip.Addr {
	if tcp, ok := c.RemoteAddr().(*net.TCPAddr); ok {
		return tcp.AddrPort().Addr().Unmap()
	}
	return netip.Addr{}
}

func newSessionID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// idleConn closes the connection when nothing is sent or received for a while.
type idleConn struct {
	net.Conn
	timeout atomic.Int64
}

func newIdleConn(c net.Conn, timeout time.Duration) *idleConn {
	ic := &idleConn{Conn: c}
	ic.setTimeout(timeout)
	return ic
}

func (c *idleConn) setTimeout(d time.Duration) {
	c.timeout.Store(int64(d))
	_ = c.SetDeadline(time.Now().Add(d))
}

func (c *idleConn) Read(b []byte) (int, error) {
	_ = c.SetDeadline(time.Now().Add(time.Duration(c.timeout.Load())))
	return c.Conn.Read(b)
}

func (c *idleConn) Write(b []byte) (int, error) {
	_ = c.SetDeadline(time.Now().Add(time.Duration(c.timeout.Load())))
	return c.Conn.Write(b)
}
