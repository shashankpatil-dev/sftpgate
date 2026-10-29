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
	"sort"
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

	// Enqueue receives every file whose folder has after_upload or
	// after_download actions. It may be nil.
	Enqueue func(vfs.FileEvent) error

	// Before runs before_upload checks. Folders with checks refuse uploads
	// while it is nil.
	Before vfs.BeforeFunc

	mu       sync.Mutex
	sessions map[string]*session
	wg       sync.WaitGroup

	// conns counts open TCP connections, logged in or not.
	conns atomic.Int64
}

// session is one logged-in SSH connection.
type session struct {
	id      string
	account *config.Account
	conn    *ssh.ServerConn
	ip      string
	method  string
	started time.Time

	// files is set once the session starts serving SFTP.
	files *vfs.Handler
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

	open := s.conns.Add(1)
	defer s.conns.Add(-1)

	// Cheap checks first, before spending any CPU on SSH crypto.
	if open > int64(cfg.Server.MaxConnections) {
		s.reject(ev, "rejected_limit", "too many connections")
		return
	}
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
	start := time.Now()
	sshConn, chans, reqs, err := ssh.NewServerConn(conn, auth.sshConfig())
	ev.User = auth.user

	// Port checks and health probes connect and leave without a word: no
	// trace for those. The spans start afterwards, backdated to the connect.
	if err != nil && isProbe(ev, err) {
		slog.Debug("connection closed before login", "ip", ip)
		return
	}
	ctx, span := tracing.Tracer.Start(context.Background(), "sftp.session", trace.WithTimestamp(start),
		trace.WithAttributes(attribute.String("client.address", ip.String())))
	ev.TraceID = tracing.TraceID(ctx)
	s.traceLogin(ctx, start, sshConn, err)
	if err != nil {
		s.loginFailed(ev, cfg, ip, err)
		tracing.Finish(span, loginResult(err), err)
		return
	}
	defer sshConn.Close()
	conn.setTimeout(cfg.Server.IdleTimeout)

	sess := &session{
		id:      newSessionID(),
		account: cfg.Account(sshConn.Permissions.Extensions[extAccount]),
		conn:    sshConn,
		ip:      ip.String(),
		method:  sshConn.Permissions.Extensions[extMethod],
		started: time.Now(),
	}
	ev.Session, ev.Account = sess.id, sess.account.Name
	ev.Method = sess.method
	ev.KeyFP = sshConn.Permissions.Extensions[extKeyFP]
	metrics.Auth.WithLabelValues(sess.account.Name, ev.Method, audit.OK).Inc()

	span.SetAttributes(
		tracing.Account.String(sess.account.Name),
		tracing.Session.String(sess.id),
		tracing.AuthMethod.String(ev.Method),
	)
	if err := s.addSession(sess); err != nil {
		ev.Event, ev.Result, ev.Error = "auth_ok", audit.Denied, err.Error()
		audit.Emit(ev)
		tracing.Finish(span, audit.Denied, err)
		return
	}
	defer s.removeSession(sess)
	defer tracing.Finish(span, audit.OK, nil)

	ev.Event, ev.Result = "session_start", audit.OK
	audit.Emit(ev)

	go ssh.DiscardRequests(reqs)
	s.serveChannels(chans, sess.handler(ctx, cfg, s))

	ev.Event, ev.Method, ev.KeyFP = "session_end", "", ""
	ev.DurationMS = time.Since(sess.started).Milliseconds()
	audit.Emit(ev)
}

// handler builds the file handler for this session. It is kept on the
// session so the API can read its byte counters.
func (sess *session) handler(ctx context.Context, cfg *config.Config, s *Server) *vfs.Handler {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess.files = &vfs.Handler{
		Config:    cfg,
		Account:   sess.account,
		Buckets:   s.Buckets,
		SessionID: sess.id,
		IP:        sess.ip,
		Enqueue:   s.Enqueue,
		Before:    s.Before,
		Ctx:       ctx,
	}
	return sess.files
}

// SessionInfo describes one open session, for the web UI.
type SessionInfo struct {
	ID       string    `json:"id"`
	Account  string    `json:"account"`
	IP       string    `json:"ip"`
	Method   string    `json:"method"`
	Started  time.Time `json:"started"`
	BytesIn  int64     `json:"bytes_in"`
	BytesOut int64     `json:"bytes_out"`
}

// Sessions lists the open sessions, oldest first.
func (s *Server) Sessions() []SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]SessionInfo, 0, len(s.sessions))
	for _, sess := range s.sessions {
		info := SessionInfo{
			ID:      sess.id,
			Account: sess.account.Name,
			IP:      sess.ip,
			Method:  sess.method,
			Started: sess.started,
		}
		if sess.files != nil {
			info.BytesIn = sess.files.BytesIn.Load()
			info.BytesOut = sess.files.BytesOut.Load()
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.Before(out[j].Started) })
	return out
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
// isProbe reports whether a connection left before saying anything, like
// a port check or a health probe.
func isProbe(ev audit.Event, err error) bool {
	return ev.User == "" && errors.Is(err, io.EOF)
}

// loginResult is "denied" for wrong credentials, "failed" for a handshake
// that broke down (for example no common encryption).
func loginResult(err error) string {
	var authErr *ssh.ServerAuthError
	if errors.As(err, &authErr) {
		return audit.Denied
	}
	return audit.Failed
}

// traceLogin records the handshake and login as an sftp.login span.
func (s *Server) traceLogin(ctx context.Context, start time.Time, conn *ssh.ServerConn, err error) {
	_, login := tracing.Tracer.Start(ctx, "sftp.login", trace.WithTimestamp(start))
	if err != nil {
		tracing.Finish(login, loginResult(err), err)
		return
	}
	login.SetAttributes(
		tracing.Account.String(conn.Permissions.Extensions[extAccount]),
		tracing.AuthMethod.String(conn.Permissions.Extensions[extMethod]),
	)
	tracing.Finish(login, audit.OK, nil)
}

func (s *Server) loginFailed(ev audit.Event, cfg *config.Config, ip netip.Addr, err error) {
	var authErr *ssh.ServerAuthError
	if !errors.As(err, &authErr) {
		// Anything else needs to be visible: a partner whose software can't
		// agree on encryption with us would otherwise fail silently.
		slog.Warn("ssh handshake failed", "ip", ip, "user", ev.User, "err", err)
		ev.Event, ev.Result, ev.Error = "auth_failed", audit.Failed, "ssh handshake failed: "+err.Error()
		audit.Emit(ev)
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
