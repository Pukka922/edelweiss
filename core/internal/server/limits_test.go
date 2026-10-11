package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// shortTimeouts keep the timeout tests fast; production uses the defaults.
var shortTimeouts = Timeouts{Read: 300 * time.Millisecond, Idle: 600 * time.Millisecond}

// The daemon runs as root on all interfaces: without timeouts, clients that
// connect and then send nothing (or only part of a request head, or never
// finish the TLS handshake) hold connections forever, before any login.
func TestDefaultTimeouts(t *testing.T) {
	env := newTestEnv(t, nil)
	srv := env.app.Server()
	if srv.ReadTimeout != DefaultReadTimeout || srv.IdleTimeout != DefaultIdleTimeout || srv.WriteTimeout != 0 {
		t.Fatalf("read %v, idle %v, write %v", srv.ReadTimeout, srv.IdleTimeout, srv.WriteTimeout)
	}
	if DefaultReadTimeout != 30*time.Second || DefaultIdleTimeout != 120*time.Second || MaxConnsPerIP != 64 {
		t.Fatalf("defaults: read %v, idle %v, conns per ip %d", DefaultReadTimeout, DefaultIdleTimeout, MaxConnsPerIP)
	}
}

// assertHangsUp checks that the server closes conn within limit.
func assertHangsUp(t *testing.T, conn net.Conn, limit time.Duration) time.Duration {
	t.Helper()
	start := time.Now()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, err := io.Copy(io.Discard, conn) // until the server hangs up (an error response may come first)
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		t.Fatalf("connection still open after %v", time.Since(start))
	}
	d := time.Since(start)
	if d > limit {
		t.Fatalf("connection closed only after %v, want within %v", d, limit)
	}
	return d
}

func TestSilentClientsAreDisconnected(t *testing.T) {
	env := newTestEnv(t, func(d *Deps) { d.Timeouts = shortTimeouts })
	host := serve(t, env.app)
	for _, tc := range []struct{ name, send string }{
		{"nothing", ""},
		{"a partial request head", "GET /api/health HTTP/1.1\r\nHost: x\r\n"},
		{"a request head without its body", "POST /api/auth/login HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn, err := net.Dial("tcp", host)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			_, _ = conn.Write([]byte(tc.send))
			if d := assertHangsUp(t, conn, 3*time.Second); d < shortTimeouts.Read/2 {
				t.Fatalf("closed after %v, before the read timeout", d)
			}
		})
	}

	// a kept-alive connection is closed once it has been idle for the idle timeout
	conn, err := net.Dial("tcp", host)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	br := bufio.NewReader(conn)
	_, _ = fmt.Fprint(conn, "GET /api/health HTTP/1.1\r\nHost: x\r\n\r\n")
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || resp.Close {
		t.Fatalf("health: %d, close %v", resp.StatusCode, resp.Close)
	}
	if d := assertHangsUp(t, conn, 3*time.Second); d < shortTimeouts.Read {
		t.Fatalf("idle connection closed after %v, before the idle timeout", d)
	}
}

// The read timeout also bounds the TLS handshake.
func TestSilentTLSClientsAreDisconnected(t *testing.T) {
	env := newTestEnv(t, func(d *Deps) { d.Config.TLS.Enabled = true; d.Timeouts = shortTimeouts })
	host := serveTLS(t, env.app)
	conn, err := net.Dial("tcp", host) // and never says hello
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	assertHangsUp(t, conn, 3*time.Second)

	// a client that does complete the handshake is served as usual
	hc := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}} //nolint:gosec // throwaway test certificate
	resp, err := hc.Get("https://" + host + "/api/health")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("health over tls: %d", resp.StatusCode)
	}
}

// loginOn logs bob in on a served app and returns the session cookie and CSRF token.
func loginOn(t *testing.T, hc *http.Client, host string) (*http.Cookie, string) {
	t.Helper()
	body := `{"username":"bob","password":"` + testPassword + `"}`
	resp, err := hc.Post("http://"+host+"/api/auth/login", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var s sessionJSON
	_ = json.NewDecoder(resp.Body).Decode(&s)
	for _, ck := range resp.Cookies() {
		if ck.Name == cookieSession && resp.StatusCode == 200 {
			return ck, s.CSRFToken
		}
	}
	t.Fatalf("login: %d", resp.StatusCode)
	return nil, ""
}

// Uploads to extensions are bounded by their pauses, not their duration: one
// that trickles in for longer than the read timeout still arrives whole.
func TestSlowUploadToAnExtension(t *testing.T) {
	env := newTestEnv(t, func(d *Deps) { d.Timeouts = shortTimeouts })
	host := serve(t, env.app)
	session, csrf := loginOn(t, &http.Client{Timeout: 5 * time.Second}, host)

	conn, err := net.Dial("tcp", host)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	const first, pieces, piece = 16 << 10, 8, 1 << 10
	payload := append(bytes.Repeat([]byte("a"), first), bytes.Repeat([]byte("b"), pieces*piece)...)
	_, _ = fmt.Fprintf(conn, "POST /ext/testext/api/echo HTTP/1.1\r\nHost: %s\r\nCookie: %s\r\n%s: %s\r\n"+
		"Content-Type: application/octet-stream\r\nContent-Length: %d\r\n\r\n", host, session.String(), headerCSRF, csrf, len(payload))
	_, _ = conn.Write(payload[:first])
	start := time.Now()
	for i := range pieces {
		time.Sleep(shortTimeouts.Read / 3)
		_, _ = conn.Write(payload[first+i*piece : first+(i+1)*piece])
	}
	if d := time.Since(start); d < 2*shortTimeouts.Read {
		t.Fatalf("the upload took only %v", d)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("no response to the slow upload: %v", err)
	}
	echoed, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != 200 || !bytes.Equal(echoed, payload) {
		t.Fatalf("slow upload: status %d, %d of %d bytes echoed (%v)", resp.StatusCode, len(echoed), len(payload), err)
	}
}

// Responses are not bounded: an event stream or a WebSocket that is quiet for
// longer than the read (and idle) timeout stays open.
func TestStreamsOutliveTheTimeouts(t *testing.T) {
	env := newTestEnv(t, func(d *Deps) { d.Timeouts = shortTimeouts })
	host := serve(t, env.app)
	hc := &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: 5 * time.Second}}
	session, csrf := loginOn(t, hc, host)
	quiet := 2 * (shortTimeouts.Read + shortTimeouts.Idle)

	req, _ := http.NewRequest(http.MethodGet, "http://"+host+"/ext/testext/api/events", nil)
	req.AddCookie(session)
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	lines := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	readUntil := func(want string) {
		t.Helper()
		timeout := time.After(5 * time.Second)
		for {
			select {
			case l, ok := <-lines:
				if !ok {
					t.Fatalf("stream was cut before %q", want)
				}
				if l == want {
					return
				}
			case <-timeout:
				t.Fatalf("did not receive %q in time", want)
			}
		}
	}
	readUntil("data: 1")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws://"+host+"/ext/testext/api/ws",
		&websocket.DialOptions{HTTPHeader: http.Header{"Cookie": {session.String()}}})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()

	time.Sleep(quiet)

	rel, _ := http.NewRequest(http.MethodPost, "http://"+host+"/ext/testext/api/release", nil)
	rel.AddCookie(session)
	rel.Header.Set(headerCSRF, csrf)
	if r, err := hc.Do(rel); err != nil || r.StatusCode != 204 {
		t.Fatalf("release: %v %v", r, err)
	}
	readUntil("data: 2")
	if err := ws.Write(ctx, websocket.MessageText, []byte("still there")); err != nil {
		t.Fatalf("websocket after a quiet spell: %v", err)
	}
	if _, data, err := ws.Read(ctx); err != nil || string(data) != "still there" {
		t.Fatalf("websocket echo after a quiet spell: %q %v", data, err)
	}
}

// flakyListener returns errs from Accept one by one, then conn, then blocks
// until it is closed.
type flakyListener struct {
	mu     sync.Mutex
	errs   []error
	conn   net.Conn
	closed chan struct{}
	once   sync.Once
}

func (f *flakyListener) Accept() (net.Conn, error) {
	f.mu.Lock()
	if len(f.errs) > 0 {
		err := f.errs[0]
		f.errs = f.errs[1:]
		f.mu.Unlock()
		return nil, err
	}
	c := f.conn
	f.conn = nil
	f.mu.Unlock()
	if c != nil {
		return c, nil
	}
	<-f.closed
	return nil, net.ErrClosed
}

func (f *flakyListener) Close() error   { f.once.Do(func() { close(f.closed) }); return nil }
func (f *flakyListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)} }

func acceptErr(err error) error {
	return &net.OpError{Op: "accept", Net: "tcp", Err: os.NewSyscallError("accept4", err)}
}

// Running out of file descriptors (or a client that aborts before it is
// accepted) is temporary: fasthttp would take such an accept error for a dead
// listener and stop serving, so the listener waits and tries again, with a
// growing pause and a log line at most once a minute. A closed listener and
// other errors end Accept.
func TestLimitListenerRetriesTemporaryAcceptErrors(t *testing.T) {
	logs := &syncBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, nil))
	server, client := net.Pipe()
	defer client.Close()
	inner := &flakyListener{closed: make(chan struct{}), conn: server, errs: []error{
		acceptErr(syscall.EMFILE), acceptErr(syscall.EMFILE), acceptErr(syscall.ENFILE), acceptErr(syscall.ECONNABORTED),
		&net.OpError{Op: "accept", Net: "tcp", Err: os.ErrDeadlineExceeded},
	}}
	ln := LimitListener(inner, 2, logger)
	start := time.Now()
	c, err := ln.Accept()
	if err != nil || c != server {
		t.Fatalf("Accept = %v, %v; want the connection after the temporary errors", c, err)
	}
	if d := time.Since(start); d < (5+10+20+40+80)*time.Millisecond {
		t.Fatalf("retried after %v: no growing pause", d)
	}
	if n := strings.Count(logs.String(), "level=WARN"); n != 1 || !strings.Contains(logs.String(), "too many open files") {
		t.Fatalf("want one warning:\n%s", logs.String())
	}

	// permanent errors end Accept at once
	boom := errors.New("boom")
	for _, want := range []error{net.ErrClosed, boom} {
		ln := LimitListener(&flakyListener{closed: make(chan struct{}), errs: []error{want}}, 2, logger)
		if _, err := ln.Accept(); !errors.Is(err, want) {
			t.Fatalf("Accept = %v, want %v", err, want)
		}
	}

	// closing the listener ends a retry pause
	errs := make([]error, 100)
	for i := range errs {
		errs[i] = acceptErr(syscall.EMFILE)
	}
	ln = LimitListener(&flakyListener{closed: make(chan struct{}), errs: errs}, 2, logger)
	done := make(chan error, 1)
	go func() { _, err := ln.Accept(); done <- err }()
	time.Sleep(50 * time.Millisecond)
	_ = ln.Close()
	select {
	case err := <-done:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("Accept after Close = %v, want net.ErrClosed", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Accept kept retrying after Close")
	}
}

// One client address may hold at most max connections; further ones are closed
// at once, without an answer (which on TLS would first wait for a handshake),
// and the slot is free again once a connection has been closed.
func TestLimitListener(t *testing.T) {
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	logs := &syncBuffer{}
	ln := LimitListener(inner, 2, slog.New(slog.NewTextHandler(logs, nil)))
	defer ln.Close()
	accepted := make(chan net.Conn, 8)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				close(accepted)
				return
			}
			accepted <- c
		}
	}()
	dial := func() net.Conn {
		t.Helper()
		c, err := net.Dial("tcp", inner.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		return c
	}
	accept := func() net.Conn {
		t.Helper()
		select {
		case c := <-accepted:
			t.Cleanup(func() { c.Close() })
			return c
		case <-time.After(5 * time.Second):
			t.Fatal("connection not accepted")
			return nil
		}
	}
	assertRefused := func(c net.Conn) {
		t.Helper()
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, err := c.Read(make([]byte, 1))
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			t.Fatal("connection beyond the limit was kept open")
		}
		if n != 0 || err == nil {
			t.Fatalf("connection beyond the limit: read %d, %v", n, err)
		}
	}
	dial()
	first := accept()
	dial()
	accept()

	assertRefused(dial())
	select {
	case c := <-accepted:
		t.Fatalf("connection beyond the limit was handed out: %v", c.RemoteAddr())
	case <-time.After(100 * time.Millisecond):
	}
	if !strings.Contains(logs.String(), "127.0.0.1") {
		t.Fatalf("refusal not logged:\n%s", logs.String())
	}

	_ = first.Close()
	_ = first.Close() // closing twice frees one slot, not two
	dial()
	accept()
	assertRefused(dial())
}
