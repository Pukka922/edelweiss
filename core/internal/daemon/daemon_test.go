package daemon

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/Pukka922/edelweiss/core/internal/config"
	"github.com/Pukka922/edelweiss/core/internal/extmgr"
	"github.com/Pukka922/edelweiss/core/internal/server"
	"github.com/Pukka922/edelweiss/core/internal/testutil"
	"github.com/Pukka922/edelweiss/core/internal/tlsutil"
)

const testHash = "$6$Gx2pQr7sLm9tVw1z$nIdtw29mh8NZeWN5kYZQLBWb6sqwVSUcBmKfBiHmUkzZCjM2ZYEV6evPEf0UhG6fpFVTqemVFZDBMtL4bXs1.."

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// runningDaemon is a daemon.Run started by a test.
type runningDaemon struct {
	base     string // http://host:port
	cancel   context.CancelFunc
	finished chan struct{} // closed when Run has returned
	err      error         // what Run returned; valid once finished is closed
}

// wait blocks until Run has returned (after cancel) and returns its error.
func (d *runningDaemon) wait(t *testing.T, timeout time.Duration) error {
	t.Helper()
	select {
	case <-d.finished:
		return d.err
	case <-time.After(timeout):
		t.Fatal("daemon did not shut down")
		return nil
	}
}

// startDaemon runs daemon.Run with TLS disabled and testext preinstalled. The
// daemon is shut down when the test ends.
func startDaemon(t *testing.T, cfg *config.Config) string {
	t.Helper()
	return startDaemonCtl(t, cfg).base
}

// startDaemonCtl is startDaemon for tests that shut the daemon down themselves.
func startDaemonCtl(t *testing.T, cfg *config.Config) *runningDaemon {
	t.Helper()
	return launchDaemon(t, cfg, false, io.Discard)
}

// insecureClient talks to a daemon with a self-signed certificate.
var insecureClient = &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
	TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // the daemon's self-signed test certificate
}}

// launchDaemon runs daemon.Run (with a self-signed certificate if useTLS) and
// testext preinstalled, logging to logs. The daemon is shut down when the
// test ends.
func launchDaemon(t *testing.T, cfg *config.Config, useTLS bool, logs io.Writer) *runningDaemon {
	t.Helper()
	base := testutil.TempDir(t)
	cfg.TLS.Enabled = useTLS
	cfg.Listen = fmt.Sprintf("127.0.0.1:%d", freePort(t))
	cfg.Paths.StateDir = filepath.Join(base, "state")
	cfg.Paths.RunDir = filepath.Join(base, "run")
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	q, _ := extmgr.NewExecQuerier()
	reg, err := extmgr.New(extmgr.Options{Dir: cfg.ExtensionsDir(), TmpDir: cfg.TmpDir(), Querier: q})
	if err != nil {
		t.Fatal(err)
	}
	bin, err := testutil.BuildTestExt()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.InstallFile(context.Background(), bin, extmgr.Source{Type: "file", Location: bin}, ""); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	d := &runningDaemon{cancel: cancel, finished: make(chan struct{})}
	logger := slog.New(slog.NewTextHandler(logs, nil))
	go func() {
		d.err = Run(ctx, cfg, logger)
		close(d.finished)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-d.finished:
			if d.err != nil {
				t.Errorf("Run: %v", d.err)
			}
		case <-time.After(20 * time.Second):
			t.Error("daemon did not shut down")
		}
	})
	d.base = "http://" + cfg.Listen
	probe := http.DefaultClient
	if useTLS {
		d.base, probe = "https://"+cfg.Listen, insecureClient
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, err := probe.Get(d.base + "/api/health")
		if err == nil && resp.StatusCode == 200 {
			resp.Body.Close()
			return d
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon did not become healthy")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// login returns an HTTP client holding the session cookie of name.
func login(t *testing.T, base, name, password string) *http.Client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 30 * time.Second}
	body, _ := json.Marshal(map[string]string{"username": name, "password": password})
	resp, err := client.Post(base+"/api/auth/login", "application/json", bytes.NewReader(body))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("login: %v %v", resp, err)
	}
	resp.Body.Close()
	return client
}

func loginAndHello(t *testing.T, base, name, password string) (osUID int) {
	t.Helper()
	client := login(t, base, name, password)
	resp, err := client.Get(base + "/ext/testext/api/hello")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("hello: %v %v", resp, err)
	}
	defer resp.Body.Close()
	var hello struct {
		User  string `json:"user"`
		OSUid int    `json:"osUid"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&hello)
	if hello.User != name {
		t.Fatalf("hello user %q", hello.User)
	}
	return hello.OSUid
}

// currentUserConfig returns a config whose only user is the current OS user,
// password "edelweiss-test".
func currentUserConfig(t *testing.T) (*config.Config, string) {
	t.Helper()
	me, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Auth.DenyUsers = nil
	cfg.Auth.ShadowFile = filepath.Join(t.TempDir(), "shadow")
	if err := os.WriteFile(cfg.Auth.ShadowFile, []byte(me.Username+":"+testHash+":20000::::::\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfg, me.Username
}

func TestDaemonEndToEndAsCurrentUser(t *testing.T) {
	cfg, name := currentUserConfig(t)
	base := startDaemon(t, cfg)
	if uid := loginAndHello(t, base, name, "edelweiss-test"); uid != os.Getuid() {
		t.Fatalf("osUid = %d", uid)
	}
}

// A request that is in flight when the daemon is told to stop must be able to
// finish: the HTTP server drains first, the extension processes are stopped
// only afterwards.
func TestShutdownDrainsRequestsBeforeStoppingExtensions(t *testing.T) {
	cfg, name := currentUserConfig(t)
	d := startDaemonCtl(t, cfg)
	client := login(t, d.base, name, "edelweiss-test")

	// testext streams "tick 1", then blocks until /release is called.
	resp, err := client.Get(d.base + "/ext/testext/api/events")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("events: %v %v", resp, err)
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

	d.cancel() // SIGTERM

	// The listener closes as the HTTP shutdown starts.
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", strings.TrimPrefix(d.base, "http://"), time.Second)
		if err != nil {
			break
		}
		c.Close()
		if time.Now().After(deadline) {
			t.Fatal("listener still open after shutdown was requested")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Give a premature StopAll ample time to kill the extension and cut the stream.
	grace := time.After(500 * time.Millisecond)
	for settled := false; !settled; {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatal("stream was cut while shutdown should still be draining (extension stopped too early)")
			}
			if l != "" { // the blank line ends the first event
				t.Fatalf("unexpected stream line %q before release", l)
			}
		case <-grace:
			settled = true
		}
	}

	// The browser cannot reach the daemon any more; release the extension on its own socket.
	socks, err := filepath.Glob(filepath.Join(cfg.SocketDir(), "testext.*.sock"))
	if err != nil || len(socks) != 1 {
		t.Fatalf("extension sockets %v (%v): extension process stopped too early", socks, err)
	}
	unix := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socks[0])
		},
	}}
	rel, err := unix.Post("http://testext/release", "text/plain", nil)
	if err != nil {
		t.Fatalf("release: %v (extension process stopped too early)", err)
	}
	rel.Body.Close()
	if rel.StatusCode != http.StatusNoContent {
		t.Fatalf("release: %d", rel.StatusCode)
	}

	readUntil("data: 2") // the in-flight request completes
	if err := d.wait(t, 15*time.Second); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if socks, _ := filepath.Glob(filepath.Join(cfg.SocketDir(), "testext.*.sock")); len(socks) != 0 {
		t.Fatalf("extension still running after Run returned: %v", socks)
	}
}

// A second `edelweissd serve` on the same directories must give up before it
// touches anything: starting its process manager would delete the sockets of
// the running daemon's extensions. Once the first daemon is gone, the lock is
// free again.
func TestSecondDaemonOnTheSameRunDirFails(t *testing.T) {
	cfg, name := currentUserConfig(t)
	d := startDaemonCtl(t, cfg)
	client := login(t, d.base, name, "edelweiss-test")
	hello := func(what string) {
		t.Helper()
		resp, err := client.Get(d.base + "/ext/testext/api/hello")
		if err != nil {
			t.Fatalf("hello %s: %v", what, err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("hello %s: %d", what, resp.StatusCode)
		}
	}
	hello("before") // the extension process now owns a socket in the run directory
	socks, _ := filepath.Glob(filepath.Join(cfg.SocketDir(), "*.sock"))
	if len(socks) != 1 {
		t.Fatalf("sockets %v", socks)
	}

	second := *cfg // the same directories and address
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- Run(ctx, &second, slog.New(slog.NewTextHandler(io.Discard, nil))) }()
	select {
	case err := <-errc:
		if err == nil || !strings.Contains(err.Error(), "edelweissd is already running") ||
			!strings.Contains(err.Error(), filepath.Join(cfg.Paths.RunDir, "edelweissd.lock")) {
			t.Fatalf("second daemon: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second daemon did not fail")
	}
	if now, _ := filepath.Glob(filepath.Join(cfg.SocketDir(), "*.sock")); !slices.Equal(now, socks) {
		t.Fatalf("sockets of the running daemon changed: %v, want %v", now, socks)
	}
	hello("after the second daemon gave up")
	// a fresh connection to the extension (the WebSocket tunnel always dials its socket)
	wsCtx, wsCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer wsCancel()
	u, _ := url.Parse(d.base)
	wsClient := *client // the session cookie; the dial is bounded by wsCtx instead of a client timeout
	wsClient.Timeout = 0
	conn, _, err := websocket.Dial(wsCtx, "ws://"+u.Host+"/ext/testext/api/ws",
		&websocket.DialOptions{HTTPClient: &wsClient})
	if err != nil {
		t.Fatalf("websocket to the extension: %v", err)
	}
	conn.Close(websocket.StatusNormalClosure, "")

	d.cancel()
	if err := d.wait(t, 15*time.Second); err != nil {
		t.Fatalf("Run: %v", err)
	}
	third := *cfg
	ctx3, cancel3 := context.WithCancel(context.Background())
	done3 := make(chan error, 1)
	go func() { done3 <- Run(ctx3, &third, slog.New(slog.NewTextHandler(io.Discard, nil))) }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, err := http.Get(d.base + "/api/health")
		if err == nil && resp.StatusCode == 200 {
			resp.Body.Close()
			break
		}
		select {
		case err := <-done3:
			t.Fatalf("daemon after the first one stopped: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon after the first one stopped did not become healthy")
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel3()
	select {
	case err := <-done3:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("daemon did not shut down")
	}
}

// With TLS (the default) the daemon serves its self-signed certificate, and its
// requests count as TLS ones: the session cookie is Secure and only an https
// Origin is the same origin.
func TestDaemonServesTLS(t *testing.T) {
	cfg, name := currentUserConfig(t)
	logs := &syncBuffer{}
	d := launchDaemon(t, cfg, true, logs)
	login := func(origin string) *http.Response {
		t.Helper()
		body := `{"username":"` + name + `","password":"edelweiss-test"}`
		req, _ := http.NewRequest(http.MethodPost, d.base+"/api/auth/login", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", origin)
		resp, err := insecureClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp
	}
	if resp := login("http://" + cfg.Listen); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("login from an http origin: %d", resp.StatusCode)
	}
	resp := login(d.base)
	if resp.StatusCode != 200 || !strings.Contains(strings.ToLower(resp.Header.Get("Set-Cookie")), "; secure") {
		t.Fatalf("login over tls: %d, cookie %q", resp.StatusCode, resp.Header.Get("Set-Cookie"))
	}
	data, err := os.ReadFile(filepath.Join(cfg.TLSDir(), "cert.pem"))
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(data)
	if block == nil || resp.TLS == nil || len(resp.TLS.PeerCertificates) == 0 || !bytes.Equal(resp.TLS.PeerCertificates[0].Raw, block.Bytes) {
		t.Fatal("the daemon does not serve its self-signed certificate")
	}
	// the operator learns that (and which) certificate was generated
	sum := sha256.Sum256(block.Bytes) // as browsers and openssl x509 -fingerprint show it
	want := fmt.Sprintf(`level=INFO msg="generated a self-signed TLS certificate" cert=%s sha256=%s`,
		filepath.Join(cfg.TLSDir(), "cert.pem"), strings.ReplaceAll(fmt.Sprintf("% X", sum), " ", ":"))
	if !strings.Contains(logs.String(), want) {
		t.Fatalf("no %q in the log:\n%s", want, logs.String())
	}
}

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// One client address may hold at most server.MaxConnsPerIP connections to the
// daemon; further ones are closed at once instead of tying it up.
func TestDaemonCapsConnectionsPerAddress(t *testing.T) {
	cfg, _ := currentUserConfig(t)
	d := startDaemonCtl(t, cfg)
	addr := strings.TrimPrefix(d.base, "http://")
	http.DefaultTransport.(*http.Transport).CloseIdleConnections() // the readiness probe's

	// served opens a connection and asks for /api/health on it; a served
	// connection stays open (keep-alive) and keeps its slot
	served := func() (net.Conn, bool) {
		t.Helper()
		c, err := net.DialTimeout("tcp", addr, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		_, _ = fmt.Fprint(c, "GET /api/health HTTP/1.1\r\nHost: x\r\n\r\n")
		resp, err := http.ReadResponse(bufio.NewReader(c), nil)
		if err != nil {
			c.Close()
			return nil, false
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return c, resp.StatusCode == 200 && !resp.Close
	}
	var held []net.Conn
	defer func() {
		for _, c := range held {
			c.Close()
		}
	}()
	// fill the slots; one may still belong to a connection that was just closed
	deadline := time.Now().Add(10 * time.Second)
	for len(held) < server.MaxConnsPerIP {
		if c, ok := served(); ok {
			held = append(held, c)
			continue
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d connections were served", len(held))
		}
		time.Sleep(20 * time.Millisecond)
	}
	for range 3 {
		if c, ok := served(); ok {
			c.Close()
			t.Fatalf("connection %d from one address was served", server.MaxConnsPerIP+1)
		}
	}
	// a closed connection frees its slot
	held[0].Close()
	held = held[1:]
	deadline = time.Now().Add(5 * time.Second)
	for {
		if c, ok := served(); ok {
			held = append(held, c)
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the slot of a closed connection was not freed")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestWatchSelfSignedLogsRenewalOnce(t *testing.T) {
	dir := t.TempDir()
	// A certificate that expires in five days is inside tlsutil's renewal window.
	cert, _, _, err := tlsutil.EnsureSelfSigned(dir, nil, time.Now().Add(-360*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	old, err := os.ReadFile(cert)
	if err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { watchSelfSigned(ctx, dir, 10*time.Millisecond, logger); close(done) }()

	deadline := time.Now().Add(10 * time.Second)
	for {
		if cur, err := os.ReadFile(cert); err == nil && !bytes.Equal(cur, old) {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("certificate was not renewed")
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond) // further ticks must not log again
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("watchSelfSigned ignored ctx")
	}
	if n := strings.Count(logs.String(), "restart edelweissd"); n != 1 {
		t.Fatalf("restart hint logged %d times:\n%s", n, logs.String())
	}
}

// recordingRunner is the commandRunner of the host power tests: it records what it is asked to run and runs nothing.
// These tests never reach a real command, whatever hostPower does.
type recordingRunner struct {
	calls []string
	out   string
	err   error
}

func (r *recordingRunner) run(name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, strings.Join(append([]string{name}, args...), " "))
	return []byte(r.out), r.err
}

func quietLogger(logs *syncBuffer) *slog.Logger { return slog.New(slog.NewTextHandler(logs, nil)) }

// In the development environment (serve --dev) the host's Power only logs: the dev container must never restart or
// stop the machine of the developer. What [dev] holds does not matter: --dev alone decides.
func TestHostPowerOnlyLogsInDevMode(t *testing.T) {
	for name, dev := range map[string]config.Dev{
		"an empty [dev] section": {Enabled: true},
		"a shell directory":      {Enabled: true, ShellDir: "/work/web/shell/dist"},
		"an extension": {Enabled: true,
			Extensions: []config.DevExtension{{Binary: "/work/ext/ext", UIDir: "/work/ext/ui/dist"}}},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := config.Default()
			cfg.Dev = dev
			var logs syncBuffer
			rec := &recordingRunner{}
			power := hostPower(cfg, quietLogger(&logs), rec.run)
			for _, action := range []string{"reboot", "poweroff"} {
				if err := power(action); err != nil {
					t.Fatalf("%s: %v", action, err)
				}
				if want := "dev mode: the host would " + action + " now"; !strings.Contains(logs.String(), want) {
					t.Fatalf("no %q in the log:\n%s", want, logs.String())
				}
			}
			if len(rec.calls) != 0 {
				t.Fatalf("dev mode ran %v", rec.calls)
			}
		})
	}
}

// Outside the development environment the two fixed absolute commands run. The guard follows Dev.Enabled (the --dev
// flag) and nothing else: [dev] settings alone do not switch it on (cli drops them without --dev anyway).
func TestHostPowerRunsTheFixedCommandsOutsideDevMode(t *testing.T) {
	for action, command := range map[string]string{"reboot": "/sbin/reboot", "poweroff": "/sbin/poweroff"} {
		cfg := config.Default()
		cfg.Dev = config.Dev{ShellDir: "/work/web/shell/dist"} // not Enabled
		rec := &recordingRunner{}
		if err := hostPower(cfg, quietLogger(&syncBuffer{}), rec.run)(action); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		if len(rec.calls) != 1 || rec.calls[0] != command {
			t.Fatalf("%s ran %v, want exactly %q", action, rec.calls, command)
		}
	}
}

func TestHostPowerRefusesOtherActions(t *testing.T) {
	for _, dev := range []bool{false, true} {
		for _, action := range []string{"", "halt", "REBOOT", "reboot -f", "/sbin/reboot", "../sbin/poweroff", "rm"} {
			cfg := config.Default()
			cfg.Dev.Enabled = dev
			rec := &recordingRunner{}
			if err := hostPower(cfg, quietLogger(&syncBuffer{}), rec.run)(action); err == nil {
				t.Fatalf("dev=%v: %q was accepted", dev, action)
			}
			if len(rec.calls) != 0 {
				t.Fatalf("dev=%v: %q ran %v", dev, action, rec.calls)
			}
		}
	}
}

func TestHostPowerReportsAFailureWithTheOutputOfTheCommand(t *testing.T) {
	failure := errors.New("exit status 1")
	rec := &recordingRunner{out: "reboot: must be run as root\n", err: failure}
	err := hostPower(config.Default(), quietLogger(&syncBuffer{}), rec.run)("reboot")
	if !errors.Is(err, failure) || !strings.Contains(err.Error(), "/sbin/reboot") ||
		!strings.Contains(err.Error(), "reboot: must be run as root") || strings.HasSuffix(err.Error(), "\n") {
		t.Fatalf("error %v", err)
	}

	rec = &recordingRunner{err: failure} // no output: just the error
	err = hostPower(config.Default(), quietLogger(&syncBuffer{}), rec.run)("poweroff")
	if !errors.Is(err, failure) || err.Error() != "/sbin/poweroff: exit status 1" {
		t.Fatalf("error %v", err)
	}
}
