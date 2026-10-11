package proxy

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/gofiber/fiber/v3"

	"github.com/Pukka922/edelweiss/sdk"
)

type upstream struct {
	sock    string
	tr      *http.Transport
	release chan struct{}
	gone    chan string // streaming handlers report their path here when they end
}

func (u *upstream) Transport() http.RoundTripper { return u.tr }
func (u *upstream) Dial(ctx context.Context) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", u.sock)
}

func newUpstream(sock string) *upstream {
	u := &upstream{sock: sock, release: make(chan struct{}), gone: make(chan string, 16)}
	u.tr = &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return u.Dial(ctx) }}
	return u
}

// streamUntilGone writes chunk every interval until the proxy goes away, then
// reports r's path on u.gone.
func (u *upstream) streamUntilGone(w http.ResponseWriter, r *http.Request, chunk []byte, interval time.Duration) {
	defer func() { u.gone <- r.URL.Path }()
	rc := http.NewResponseController(w)
	for {
		if _, err := w.Write(chunk); err != nil {
			return
		}
		if err := rc.Flush(); err != nil {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(interval):
		}
	}
}

func (u *upstream) waitGone(t *testing.T, path string) {
	t.Helper()
	select {
	case p := <-u.gone:
		if p != path {
			t.Fatalf("upstream handler %s ended, want %s", p, path)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("upstream exchange %s was not torn down", path)
	}
}

func startUpstream(t *testing.T) *upstream {
	t.Helper()
	u := newUpstream(filepath.Join(t.TempDir(), "up.sock"))
	ln, err := net.Listen("unix", u.sock)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/headers", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Set-Cookie", "edelweiss_session=evil")
		w.Header().Set("X-Upstream", "yes")
		_ = json.NewEncoder(w).Encode(r.Header)
	})
	mux.HandleFunc("/query", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, r.URL.Path+"?"+r.URL.RawQuery)
	})
	mux.HandleFunc("/sse", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		fmt.Fprint(w, "event: tick\ndata: 1\n\n")
		fl.Flush()
		select {
		case <-u.release:
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, "event: tick\ndata: 2\n\n")
		fl.Flush()
	})
	mux.HandleFunc("/big", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("x"), 2<<20))
	})
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(w, r.Body)
	})
	// echoes the request body piece by piece as it arrives: the response is under
	// way while the upload goes on (net/http would otherwise drop the unread rest
	// of the body once the response head is out)
	mux.HandleFunc("/duplex", func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w)
		_ = rc.EnableFullDuplex()
		buf := make([]byte, 4<<10)
		for {
			n, err := r.Body.Read(buf)
			if n > 0 {
				_, _ = w.Write(buf[:n])
				_ = rc.Flush()
			}
			if err != nil {
				return
			}
		}
	})
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer c.CloseNow()
		for {
			typ, data, err := c.Read(r.Context())
			if err != nil {
				return
			}
			if err := c.Write(r.Context(), typ, data); err != nil {
				return
			}
		}
	})
	// reports how the request body was framed and how much of it arrived
	mux.HandleFunc("/framing", func(w http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body)
		fmt.Fprintf(w, "cl=%d te=%v got=%d", r.ContentLength, r.TransferEncoding, n)
	})
	// reads the start of the upload, stops reading for ms milliseconds (busy
	// elsewhere), then reads the rest and reports how much arrived
	mux.HandleFunc("/pause", func(w http.ResponseWriter, r *http.Request) {
		ms, _ := strconv.Atoi(r.URL.Query().Get("ms"))
		head, _ := io.CopyN(io.Discard, r.Body, 64<<10)
		time.Sleep(time.Duration(ms) * time.Millisecond)
		rest, _ := io.Copy(io.Discard, r.Body)
		fmt.Fprintf(w, "got=%d", head+rest)
	})
	// declare more bytes than are sent: the upstream connection is cut short
	mux.HandleFunc("/short", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte("0123456789"))
	})
	mux.HandleFunc("/cut", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(2<<20))
		_, _ = w.Write(bytes.Repeat([]byte("c"), 1<<20))
	})
	// answers immediately, without reading the request body
	mux.HandleFunc("/reject", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		fmt.Fprint(w, "no")
	})
	mux.HandleFunc("/wsheaders", func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer c.CloseNow()
		data, _ := json.Marshal(map[string]any{"header": r.Header, "host": r.Host, "uri": r.RequestURI})
		_ = c.Write(r.Context(), websocket.MessageText, data)
		_, _, _ = c.Read(r.Context()) // wait for the client to close
	})
	// answers at once with an endless stream, never reading the request body
	mux.HandleFunc("/stream-early", func(w http.ResponseWriter, r *http.Request) {
		u.streamUntilGone(w, r, bytes.Repeat([]byte("s"), 4<<10), 5*time.Millisecond)
	})
	// answers at once with a short stream, never reading the request body
	mux.HandleFunc("/stream-reject", func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w)
		for range 3 {
			_, _ = w.Write(bytes.Repeat([]byte("r"), 1<<10))
			_ = rc.Flush()
		}
	})
	mux.HandleFunc("/ticker", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		u.streamUntilGone(w, r, []byte("data: tick\n\n"), 20*time.Millisecond)
	})
	// a resource of n bytes with an explicit Content-Length (HEAD included)
	mux.HandleFunc("/sized", func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.URL.Query().Get("n"))
		w.Header().Set("Content-Length", strconv.Itoa(n))
		if r.Method != http.MethodHead {
			_, _ = w.Write(bytes.Repeat([]byte("s"), n))
		}
	})
	mux.HandleFunc("/wscookie", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Set-Cookie", "edelweiss_session=evil")
		w.Header().Set("X-Upstream", "yes")
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer c.CloseNow()
		typ, data, err := c.Read(r.Context())
		if err != nil {
			return
		}
		_ = c.Write(r.Context(), typ, data)
		_, _, _ = c.Read(r.Context()) // wait for the client to close
	})
	// refuses the upgrade with an ordinary HTTP answer
	mux.HandleFunc("/wsreject", func(w http.ResponseWriter, r *http.Request) {
		code, _ := strconv.Atoi(r.URL.Query().Get("code"))
		w.Header().Set("Set-Cookie", "edelweiss_session=evil")
		http.Error(w, "nope", code)
	})
	// answers the upgrade with something that is not a WebSocket handshake
	mux.HandleFunc("/wsraw", func(w http.ResponseWriter, r *http.Request) {
		conn, brw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		switch r.URL.Query().Get("mode") {
		case "other":
			_, _ = brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: h2c\r\nConnection: Upgrade\r\nSet-Cookie: edelweiss_session=evil\r\n\r\n")
			_ = brw.Flush()
			time.Sleep(time.Second) // keep the "tunnel" open: it must not be handed to the client
		case "hangup":
			// a valid handshake, one frame sent along with it, then the extension goes away
			key := r.Header.Get("Sec-WebSocket-Key")
			sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
			_, _ = brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
				"Sec-WebSocket-Accept: " + base64.StdEncoding.EncodeToString(sum[:]) + "\r\n\r\n\x81\x03bye")
			_ = brw.Flush()
		}
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return u
}

func startProxy(t *testing.T, up Upstream, releases *atomic.Int32) string {
	t.Helper()
	return startProxyStream(t, up, releases, true)
}

// startProxyStream is startProxy with request body streaming switched on or off
// (off: fasthttp buffers the whole body, so Forward takes its in-memory path).
func startProxyStream(t *testing.T, up Upstream, releases *atomic.Int32, stream bool) string {
	t.Helper()
	return startProxyConfig(t, up, releases, fiber.Config{StreamRequestBody: stream, BodyLimit: 64 << 20})
}

func startProxyConfig(t *testing.T, up Upstream, releases *atomic.Int32, cfg fiber.Config) string {
	t.Helper()
	cfg.ErrorHandler = sdk.ErrorHandler
	app := fiber.New(cfg)
	id := Identity{User: "alice", UID: 1000, Admin: true, Lang: "de", RequestID: "req-1"}
	// The exchange must end (and release be called) while the request still owns
	// its fasthttp context: the request body cleanup acts on its connection.
	var late atomic.Int32
	t.Cleanup(func() {
		if n := late.Load(); n > 0 {
			t.Errorf("release called %d times after fasthttp recycled the request context", n)
		}
	})
	app.All("/p/*", func(c fiber.Ctx) error {
		target := "/" + c.Params("*")
		if q := string(c.Request().URI().QueryString()); q != "" {
			target += "?" + q
		}
		rc := c.RequestCtx()
		conn := rc.Conn()
		release := func() {
			if rc.Conn() != conn {
				late.Add(1)
			}
			releases.Add(1)
		}
		if IsUpgrade(c) {
			return ForwardWebSocket(c, up, target, id, release)
		}
		return Forward(c, up, target, id, release)
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go app.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true})
	// a bounded shutdown keeps a failing streaming test from hanging on its open stream
	t.Cleanup(func() { _ = app.ShutdownWithTimeout(2 * time.Second) })
	return "http://" + ln.Addr().String()
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestForwardHeaders(t *testing.T) {
	var releases atomic.Int32
	base := startProxy(t, startUpstream(t), &releases)
	req, _ := http.NewRequest("GET", base+"/p/headers", nil)
	req.Header.Set("Cookie", "edelweiss_session=secret")
	req.Header.Set("Authorization", "Bearer x")
	req.Header.Set("X-Csrf-Token", "tok")
	req.Header.Set("X-Edelweiss-User", "mallory")
	req.Header.Set("X-Edelweiss-Admin", "0")
	req.Header.Set("X-Custom", "keep")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got http.Header
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"Cookie", "Authorization", "X-Csrf-Token"} {
		if got.Get(h) != "" {
			t.Errorf("%s leaked to upstream: %q", h, got.Get(h))
		}
	}
	want := map[string]string{
		sdk.HeaderUser: "alice", sdk.HeaderUID: "1000", sdk.HeaderAdmin: "1",
		sdk.HeaderLang: "de", sdk.HeaderRequestID: "req-1", "X-Custom": "keep",
	}
	for k, v := range want {
		if vals := got.Values(k); len(vals) != 1 || vals[0] != v {
			t.Errorf("%s = %v, want [%s]", k, vals, v)
		}
	}
	if resp.Header.Get("X-Upstream") != "yes" || resp.Header.Get("Set-Cookie") != "" {
		t.Errorf("response headers: %v", resp.Header)
	}
	waitFor(t, "release", func() bool { return releases.Load() == 1 })
}

func TestForwardQuery(t *testing.T) {
	var releases atomic.Int32
	base := startProxy(t, startUpstream(t), &releases)
	resp, err := http.Get(base + "/p/query?a=1&b=two")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "/query?a=1&b=two" {
		t.Fatalf("body %q", body)
	}
	waitFor(t, "release", func() bool { return releases.Load() == 1 })
}

func TestForwardStreamsSSE(t *testing.T) {
	var releases atomic.Int32
	up := startUpstream(t)
	base := startProxy(t, up, &releases)
	// a buffering proxy would withhold the response headers: fail fast instead of hanging
	client := &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: 3 * time.Second}}
	resp, err := client.Get(base + "/p/sse")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type %q", ct)
	}
	lines := make(chan string, 10)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	readUntil := func(want string) {
		t.Helper()
		timeout := time.After(3 * time.Second)
		for {
			select {
			case l, ok := <-lines:
				if !ok {
					t.Fatalf("stream closed before %q", want)
				}
				if l == want {
					return
				}
			case <-timeout:
				t.Fatalf("did not receive %q in time (response buffered?)", want)
			}
		}
	}
	readUntil("data: 1") // arrives while upstream is still blocked
	if releases.Load() != 0 {
		t.Fatal("released while stream still open")
	}
	close(up.release)
	readUntil("data: 2")
	waitFor(t, "release after stream end", func() bool { return releases.Load() == 1 })
}

func TestForwardLargeBodies(t *testing.T) {
	var releases atomic.Int32
	base := startProxy(t, startUpstream(t), &releases)
	resp, err := http.Get(base + "/p/big")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if len(body) != 2<<20 {
		t.Fatalf("got %d bytes", len(body))
	}
	upload := bytes.Repeat([]byte("abcdefgh"), 5<<17) // 5 MiB
	resp, err = http.Post(base+"/p/echo", "application/octet-stream", bytes.NewReader(upload))
	if err != nil {
		t.Fatal(err)
	}
	echoed, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Equal(echoed, upload) {
		t.Fatalf("echo mismatch: %d bytes", len(echoed))
	}
	waitFor(t, "releases", func() bool { return releases.Load() == 2 })
}

func TestForwardUpstreamDown(t *testing.T) {
	var releases atomic.Int32
	base := startProxy(t, newUpstream(filepath.Join(t.TempDir(), "missing.sock")), &releases)
	resp, err := http.Get(base + "/p/headers")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var e struct{ Error struct{ Code string } }
	_ = json.NewDecoder(resp.Body).Decode(&e)
	if resp.StatusCode != 502 || e.Error.Code != "backend_unavailable" {
		t.Fatalf("status %d code %q", resp.StatusCode, e.Error.Code)
	}
	if releases.Load() != 1 {
		t.Fatalf("releases = %d", releases.Load())
	}
}

func TestWebSocketTunnel(t *testing.T) {
	var releases atomic.Int32
	base := startProxy(t, startUpstream(t), &releases)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, strings.Replace(base, "http", "ws", 1)+"/p/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ctx, websocket.MessageText, []byte("ping")); err != nil {
		t.Fatal(err)
	}
	typ, data, err := conn.Read(ctx)
	if err != nil || typ != websocket.MessageText || string(data) != "ping" {
		t.Fatalf("read %v %q %v", typ, data, err)
	}
	conn.Close(websocket.StatusNormalClosure, "")
	waitFor(t, "release after websocket close", func() bool { return releases.Load() == 1 })
}

func TestWebSocketRejectsForeignOrigin(t *testing.T) {
	var releases atomic.Int32
	base := startProxy(t, startUpstream(t), &releases)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(ctx, strings.Replace(base, "http", "ws", 1)+"/p/ws",
		&websocket.DialOptions{HTTPHeader: http.Header{"Origin": {"http://evil.example"}}})
	if err == nil || resp == nil || resp.StatusCode != 403 {
		t.Fatalf("expected 403, got resp=%v err=%v", resp, err)
	}
	if releases.Load() != 1 {
		t.Fatalf("releases = %d", releases.Load())
	}
}

// Both request-body paths (fasthttp streaming on and off) forward the bytes the
// client sent; fiber's c.Body() would have gunzipped this one.
func TestForwardKeepsRequestBodyEncoding(t *testing.T) {
	for _, stream := range []bool{true, false} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			var releases atomic.Int32
			base := startProxyStream(t, startUpstream(t), &releases, stream)
			var buf bytes.Buffer
			zw := gzip.NewWriter(&buf)
			_, _ = zw.Write([]byte(strings.Repeat("hello ", 20)))
			_ = zw.Close()
			req, _ := http.NewRequest("POST", base+"/p/echo", bytes.NewReader(buf.Bytes()))
			req.Header.Set("Content-Encoding", "gzip")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			echoed, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if !bytes.Equal(echoed, buf.Bytes()) {
				t.Fatalf("request body was altered: %d bytes echoed, %d sent", len(echoed), buf.Len())
			}
			waitFor(t, "release", func() bool { return releases.Load() == 1 })
		})
	}
}

func TestForwardRequestBodyFraming(t *testing.T) {
	for _, stream := range []bool{true, false} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			var releases atomic.Int32
			base := startProxyStream(t, startUpstream(t), &releases, stream)
			// streamed: relayed chunked; buffered by fasthttp: forwarded with its real length
			chunkedWant := "cl=12 te=[] got=12"
			if stream {
				chunkedWant = "cl=-1 te=[chunked] got=12"
			}
			pr, pw := io.Pipe()
			go func() { _, _ = pw.Write([]byte("chunked-body")); _ = pw.Close() }()
			cases := []struct {
				name string
				body io.Reader
				want string
			}{
				{"empty", http.NoBody, "cl=0 te=[] got=0"},
				{"small", strings.NewReader("hello"), "cl=5 te=[] got=5"},
				{"large", bytes.NewReader(bytes.Repeat([]byte("y"), 300<<10)), "cl=307200 te=[] got=307200"},
				{"chunked", pr, chunkedWant},
			}
			for _, tc := range cases {
				resp, err := http.Post(base+"/p/framing", "application/octet-stream", tc.body)
				if err != nil {
					t.Fatalf("%s: %v", tc.name, err)
				}
				got, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if string(got) != tc.want {
					t.Errorf("%s: upstream saw %q, want %q", tc.name, got, tc.want)
				}
			}
			waitFor(t, "releases", func() bool { return int(releases.Load()) == len(cases) })
		})
	}
}

// The upstream answers before it has read the upload while the client stalls
// mid-body. The proxy must not leave the transport reading from the recycled
// request stream (data race / fasthttp panic), must answer, and must not keep
// the half-read connection alive.
func TestForwardEarlyResponseStalledUpload(t *testing.T) {
	var releases atomic.Int32
	base := startProxy(t, startUpstream(t), &releases)
	conn, err := net.Dial("tcp", strings.TrimPrefix(base, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = fmt.Fprintf(conn, "POST /p/reject HTTP/1.1\r\nHost: x\r\nContent-Type: application/octet-stream\r\nContent-Length: %d\r\n\r\n", 1<<20)
	_, _ = conn.Write(bytes.Repeat([]byte("z"), 64<<10)) // ...and then stall
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	start := time.Now()
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	t.Logf("answered after %v", time.Since(start))
	if resp.StatusCode != http.StatusRequestEntityTooLarge || string(body) != "no" {
		t.Fatalf("status %d body %q", resp.StatusCode, body)
	}
	if !resp.Close {
		t.Error("connection must not be reused after a cut-short upload")
	}
	waitFor(t, "release", func() bool { return releases.Load() == 1 })

	// the proxy is still healthy
	r2, err := http.Get(base + "/p/query?x=1")
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := io.ReadAll(r2.Body)
	r2.Body.Close()
	if string(b2) != "/query?x=1" {
		t.Fatalf("body %q", b2)
	}
}

func TestWebSocketForwardsSanitizedUpgrade(t *testing.T) {
	var releases atomic.Int32
	base := startProxy(t, startUpstream(t), &releases)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, strings.Replace(base, "http", "ws", 1)+"/p/wsheaders?a=1&b=two",
		&websocket.DialOptions{HTTPHeader: http.Header{
			"Origin":           {base}, // same origin is accepted
			"Cookie":           {"edelweiss_session=secret"},
			"Authorization":    {"Bearer x"},
			"X-Csrf-Token":     {"tok"},
			"X-Edelweiss-User": {"mallory"}, "X-Edelweiss-Admin": {"0"},
			"X-Custom": {"keep"},
		}})
	if err != nil {
		t.Fatal(err)
	}
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Header http.Header
		Host   string
		URI    string
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"Cookie", "Authorization", "X-Csrf-Token"} {
		if got.Header.Get(h) != "" {
			t.Errorf("%s leaked to upstream: %q", h, got.Header.Get(h))
		}
	}
	want := map[string]string{
		sdk.HeaderUser: "alice", sdk.HeaderUID: "1000", sdk.HeaderAdmin: "1",
		sdk.HeaderLang: "de", sdk.HeaderRequestID: "req-1", "X-Custom": "keep",
	}
	for k, v := range want {
		if vals := got.Header.Values(k); len(vals) != 1 || vals[0] != v {
			t.Errorf("%s = %v, want [%s]", k, vals, v)
		}
	}
	if got.URI != "/wsheaders?a=1&b=two" || got.Host != "edelweiss-ext" {
		t.Errorf("uri %q host %q", got.URI, got.Host)
	}
	conn.Close(websocket.StatusNormalClosure, "")
	waitFor(t, "release after websocket close", func() bool { return releases.Load() == 1 })
}

// The extension dies while a response with a known length is being relayed.
func TestForwardUpstreamCutShort(t *testing.T) {
	var releases atomic.Int32
	base := startProxy(t, startUpstream(t), &releases)

	// buffered response: answer with a clean error, not with the half-copied headers
	resp, err := http.Get(base + "/p/short")
	if err != nil {
		t.Fatal(err)
	}
	var e struct{ Error struct{ Code string } }
	_ = json.NewDecoder(resp.Body).Decode(&e)
	resp.Body.Close()
	if resp.StatusCode != 502 || e.Error.Code != "backend_unavailable" || resp.Header.Get("Content-Encoding") != "" {
		t.Errorf("buffered: status %d code %q headers %v", resp.StatusCode, e.Error.Code, resp.Header)
	}

	// streamed response: the client must see the failure, not a clean end of body
	resp, err = http.Get(base + "/p/cut")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err == nil {
		t.Errorf("streamed: truncated body (%d of %d bytes) looked complete", len(got), 2<<20)
	}
	waitFor(t, "releases", func() bool { return releases.Load() == 2 })
}

func dialProxy(t *testing.T, base string) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", strings.TrimPrefix(base, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	return conn
}

// assertClosed checks that the proxy closed the connection instead of reading
// another request from it.
func assertClosed(t *testing.T, conn net.Conn, r io.Reader) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, err := r.Read(make([]byte, 64))
	var ne net.Error
	if n > 0 || err == nil || (errors.As(err, &ne) && ne.Timeout()) {
		t.Errorf("connection still open after the response: read %d bytes, err %v", n, err)
	}
}

func assertHealthy(t *testing.T, base string) {
	t.Helper()
	resp, err := http.Get(base + "/p/query?x=1")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "/query?x=1" {
		t.Fatalf("body %q", body)
	}
}

// The extension streams its answer before it has read the upload, the client
// stalls mid-upload and then drops the connection (reset, like a closed tab).
// fasthttp reclaims the request body stream as soon as it is done with the
// response, so the transport must have let go of it by then (run with -race);
// the upstream exchange must be torn down and release called once.
func TestForwardClientGoneDuringStreamedUpload(t *testing.T) {
	var releases atomic.Int32
	up := startUpstream(t)
	base := startProxy(t, up, &releases)
	for i := range 3 {
		conn := dialProxy(t, base)
		_, _ = fmt.Fprintf(conn, "POST /p/stream-early HTTP/1.1\r\nHost: x\r\nContent-Type: application/octet-stream\r\nContent-Length: %d\r\n\r\n", 1<<20)
		_, _ = conn.Write(bytes.Repeat([]byte("z"), 64<<10)) // ...and then stall
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.ReadFull(resp.Body, make([]byte, 8<<10)); err != nil {
			t.Fatalf("streamed body: %v", err)
		}
		if !resp.Close {
			t.Error("connection must not be reused: the extension answered before it read the upload")
		}
		_ = conn.(*net.TCPConn).SetLinger(0) // reset instead of an orderly close
		_ = conn.Close()
		up.waitGone(t, "/stream-early")
		waitFor(t, "release", func() bool { return releases.Load() == int32(i+1) })
	}
	assertHealthy(t, base)
}

// Same, but the client vanishes without a reset: the write to it times out
// while the transport is still blocked waiting for upload bytes. fasthttp
// recycles the request body (and the connection's reader) right after the
// failed write, so the transport must have been cut loose before (run with
// -race; this is where a stream writer goroutine outlived the RequestCtx).
func TestForwardWriteTimeoutDuringStreamedUpload(t *testing.T) {
	var releases atomic.Int32
	up := startUpstream(t)
	base := startProxyConfig(t, up, &releases, fiber.Config{
		StreamRequestBody: true, BodyLimit: 64 << 20, WriteTimeout: 300 * time.Millisecond,
	})
	for i := range 2 {
		conn := dialProxy(t, base)
		_, _ = fmt.Fprintf(conn, "POST /p/stream-early HTTP/1.1\r\nHost: x\r\nContent-Type: application/octet-stream\r\nContent-Length: %d\r\n\r\n", 1<<20)
		_, _ = conn.Write(bytes.Repeat([]byte("z"), 64<<10)) // ...and then stall
		up.waitGone(t, "/stream-early")
		waitFor(t, "release", func() bool { return releases.Load() == int32(i+1) })
		_, _ = io.Copy(io.Discard, conn) // the proxy hangs up on the client
	}
	assertHealthy(t, base)
}

// With a read timeout on the server, an upload to an extension is bounded by
// its pauses, not by its duration: as long as data keeps arriving, the upload
// may take longer than the read timeout (large images over a slow link).
func TestForwardSlowUploadOutlivesTheReadTimeout(t *testing.T) {
	const readTimeout = 300 * time.Millisecond
	for _, upstreamPath := range []string{"/framing", "/duplex"} { // answers after / while reading the upload
		t.Run(upstreamPath, func(t *testing.T) {
			var releases atomic.Int32
			base := startProxyConfig(t, startUpstream(t), &releases, fiber.Config{
				StreamRequestBody: true, BodyLimit: 64 << 20, ReadTimeout: readTimeout,
			})
			conn := dialProxy(t, base)
			const first, pieces, piece = 16 << 10, 8, 1 << 10 // fasthttp reads up to 8 KiB before the handler runs
			total := first + pieces*piece
			_, _ = fmt.Fprintf(conn, "POST /p%s HTTP/1.1\r\nHost: x\r\nContent-Length: %d\r\n\r\n", upstreamPath, total)
			_, _ = conn.Write(bytes.Repeat([]byte("a"), first))
			start := time.Now()
			for range pieces {
				time.Sleep(readTimeout / 3)
				_, _ = conn.Write(bytes.Repeat([]byte("b"), piece))
			}
			if d := time.Since(start); d < 2*readTimeout {
				t.Fatalf("the upload took only %v", d)
			}
			resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
			if err != nil {
				t.Fatalf("no response to the slow upload: %v", err)
			}
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			want := fmt.Sprintf("cl=%d te=[] got=%d", total, total)
			if upstreamPath == "/duplex" {
				want = strings.Repeat("a", first) + strings.Repeat("b", pieces*piece)
			}
			if err != nil || resp.StatusCode != http.StatusOK || string(body) != want {
				t.Fatalf("slow upload: status %d, %d bytes (%v), want %d", resp.StatusCode, len(body), err, len(want))
			}
			waitFor(t, "release", func() bool { return releases.Load() == 1 })
		})
	}
}

// The read timeout measures the client, not the extension: when the extension
// stops reading the upload for longer than the read timeout (the client's data
// waits in the buffers meanwhile), the upload goes on once it reads again.
func TestForwardUploadSurvivesAPausingExtension(t *testing.T) {
	const readTimeout = 300 * time.Millisecond
	var releases atomic.Int32
	base := startProxyConfig(t, startUpstream(t), &releases, fiber.Config{
		StreamRequestBody: true, BodyLimit: 64 << 20, ReadTimeout: readTimeout,
	})
	// larger than what the socket buffers between client, proxy and extension
	// hold, so the proxy stops reading from the client during the pause
	const size = 8 << 20
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(fmt.Sprintf("%s/p/pause?ms=%d", base, 3*readTimeout/time.Millisecond),
		"application/octet-stream", bytes.NewReader(make([]byte, size)))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != fmt.Sprintf("got=%d", size) {
		t.Fatalf("upload to a pausing extension: status %d, %q", resp.StatusCode, body)
	}
	waitFor(t, "release", func() bool { return releases.Load() == 1 })
}

// ...but an upload that stalls for longer than the read timeout is cut off, and
// the connection is not used for another request (the rest of the upload would
// be parsed as one).
func TestForwardStalledUploadHitsTheReadTimeout(t *testing.T) {
	const readTimeout = 300 * time.Millisecond
	var releases atomic.Int32
	up := startUpstream(t)
	base := startProxyConfig(t, up, &releases, fiber.Config{
		StreamRequestBody: true, BodyLimit: 64 << 20, ReadTimeout: readTimeout,
	})
	conn := dialProxy(t, base)
	_, _ = fmt.Fprintf(conn, "POST /p/framing HTTP/1.1\r\nHost: x\r\nContent-Length: %d\r\n\r\n", 1<<20)
	_, _ = conn.Write(bytes.Repeat([]byte("z"), 64<<10)) // ...and then stall
	start := time.Now()
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("no answer to the stalled upload: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway || !resp.Close {
		t.Fatalf("stalled upload: status %d, close %v", resp.StatusCode, resp.Close)
	}
	if d := time.Since(start); d > 5*readTimeout {
		t.Fatalf("stalled upload was cut after %v", d)
	}
	assertClosed(t, conn, br)
	waitFor(t, "release", func() bool { return releases.Load() == 1 })
	assertHealthy(t, base)
}

// A browser closes an SSE stream: the proxy notices at the next event, tears
// the upstream exchange down and calls release.
func TestForwardSSEClientDisconnect(t *testing.T) {
	var releases atomic.Int32
	up := startUpstream(t)
	base := startProxy(t, up, &releases)
	// bounded: a buffering proxy must fail this test, not hang it
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{ResponseHeaderTimeout: 3 * time.Second}}
	resp, err := client.Get(base + "/p/ticker")
	if err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil || line != "data: tick\n" {
		t.Fatalf("first event %q, %v", line, err)
	}
	if releases.Load() != 0 {
		t.Fatal("released while stream still open")
	}
	resp.Body.Close() // unfinished body: net/http drops the connection
	up.waitGone(t, "/ticker")
	waitFor(t, "release after client disconnect", func() bool { return releases.Load() == 1 })
}

// Forwarding headers describe proxies edelweissd does not have: whatever a
// client claims in them (its address, the host or scheme it used) must not
// reach an extension, over HTTP or WebSocket.
func TestForwardDropsForwardingHeaders(t *testing.T) {
	forwarding := map[string]string{
		"X-Forwarded-For": "10.0.0.1", "X-Forwarded-Host": "evil.example", "X-Forwarded-Proto": "https",
		"Forwarded": "for=10.0.0.1;proto=https", "X-Real-Ip": "10.0.0.1",
	}
	var releases atomic.Int32
	base := startProxy(t, startUpstream(t), &releases)

	req, _ := http.NewRequest("GET", base+"/p/headers", nil)
	for k, v := range forwarding {
		req.Header.Set(k, v)
	}
	req.Header.Add("x-forwarded-for", "10.0.0.2") // any spelling
	req.Header.Set("X-Custom", "keep")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var got http.Header
	err = json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	for h := range forwarding {
		if vals := got.Values(h); len(vals) != 0 {
			t.Errorf("http: %s reached the extension: %v", h, vals)
		}
	}
	if got.Get("X-Custom") != "keep" {
		t.Errorf("http: X-Custom = %q", got.Get("X-Custom"))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	h := http.Header{"X-Custom": {"keep"}}
	for k, v := range forwarding {
		h.Set(k, v)
	}
	conn, _, err := websocket.Dial(ctx, strings.Replace(base, "http", "ws", 1)+"/p/wsheaders", &websocket.DialOptions{HTTPHeader: h})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ws struct{ Header http.Header }
	if err := json.Unmarshal(data, &ws); err != nil {
		t.Fatal(err)
	}
	for h := range forwarding {
		if vals := ws.Header.Values(h); len(vals) != 0 {
			t.Errorf("websocket: %s reached the extension: %v", h, vals)
		}
	}
	if ws.Header.Get("X-Custom") != "keep" {
		t.Errorf("websocket: X-Custom = %q", ws.Header.Get("X-Custom"))
	}
	conn.Close(websocket.StatusNormalClosure, "")
	waitFor(t, "releases", func() bool { return releases.Load() == 2 })
}

// The extension answers with a stream before it has read the upload and never
// reads the rest. The unread upload must not be parsed as the next request, and
// the response head (sent before the body) is the last chance to say so.
func TestForwardStreamedEarlyAnswerClosesConnection(t *testing.T) {
	var releases atomic.Int32
	base := startProxy(t, startUpstream(t), &releases)
	conn := dialProxy(t, base)
	go func() {
		_, _ = fmt.Fprintf(conn, "POST /p/stream-reject HTTP/1.1\r\nHost: x\r\nContent-Length: %d\r\n\r\n", 1<<20)
		_, _ = conn.Write(bytes.Repeat([]byte("z"), 1<<20))
	}()
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil || string(body) != strings.Repeat("r", 3<<10) {
		t.Errorf("body: %d bytes, err %v", len(body), err)
	}
	if !resp.Close {
		t.Error("connection must not be reused after a cut-short upload")
	}
	assertClosed(t, conn, br)
	waitFor(t, "release", func() bool { return releases.Load() == 1 })
}

// HEAD answers carry the extension's Content-Length and no body (and no body
// framing either), whatever the length; the connection stays in sync.
func TestForwardHead(t *testing.T) {
	var releases atomic.Int32
	base := startProxy(t, startUpstream(t), &releases)
	conn := dialProxy(t, base)
	br := bufio.NewReader(conn)
	for _, n := range []int{100, 5 << 20} {
		_, _ = fmt.Fprintf(conn, "HEAD /p/sized?n=%d HTTP/1.1\r\nHost: x\r\n\r\n", n)
		resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodHead})
		if err != nil {
			t.Fatalf("n=%d: %v", n, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || resp.ContentLength != int64(n) {
			t.Errorf("n=%d: status %d, content-length %d", n, resp.StatusCode, resp.ContentLength)
		}
	}
	_, _ = fmt.Fprint(conn, "GET /p/query?after=head HTTP/1.1\r\nHost: x\r\n\r\n")
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("request after HEAD: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "/query?after=head" {
		t.Fatalf("body %q", body)
	}
	waitFor(t, "releases", func() bool { return releases.Load() == 3 })
}

// A spoofed request id is replaced; hop-by-hop headers (including the ones the
// client names in Connection) and Expect (already answered by the server) stay
// at the proxy.
func TestForwardDropsHopByHopHeaders(t *testing.T) {
	var releases atomic.Int32
	base := startProxy(t, startUpstream(t), &releases)
	req, _ := http.NewRequest("POST", base+"/p/headers", strings.NewReader("{}"))
	req.Header.Set("X-Request-Id", "spoofed")
	req.Header.Set("Connection", "X-Hop")
	req.Header.Set("X-Hop", "secret")
	req.Header.Set("Expect", "100-continue")
	req.Header.Set("Proxy-Authorization", "Basic eDp5")
	req.Header.Set("Te", "trailers")
	req.Header.Set("X-Custom", "keep")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got http.Header
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if vals := got.Values(sdk.HeaderRequestID); len(vals) != 1 || vals[0] != "req-1" {
		t.Errorf("%s = %v, want [req-1]", sdk.HeaderRequestID, vals)
	}
	for _, h := range []string{"X-Hop", "Expect", "Proxy-Authorization", "Te", "Connection"} {
		if got.Get(h) != "" {
			t.Errorf("%s reached the extension: %q", h, got.Get(h))
		}
	}
	if got.Get("X-Custom") != "keep" {
		t.Errorf("X-Custom = %q", got.Get("X-Custom"))
	}
	waitFor(t, "release", func() bool { return releases.Load() == 1 })
}

const wsHandshakeHeaders = "Upgrade: websocket\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n"

// readFinalResponse skips informational (100 Continue) answers.
func readFinalResponse(t *testing.T, br *bufio.Reader) *http.Response {
	t.Helper()
	for {
		resp, err := http.ReadResponse(br, nil)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusContinue {
			return resp
		}
	}
}

// readFrame reads one unmasked, unfragmented server frame and returns its payload.
func readFrame(t *testing.T, r io.Reader) []byte {
	t.Helper()
	var h [2]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		t.Fatal(err)
	}
	n := uint64(h[1] & 0x7f)
	switch n {
	case 126:
		var b [2]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			t.Fatal(err)
		}
		n = uint64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			t.Fatal(err)
		}
		n = binary.BigEndian.Uint64(b[:])
	}
	data := make([]byte, n)
	if _, err := io.ReadFull(r, data); err != nil {
		t.Fatal(err)
	}
	return data
}

func TestWebSocketHandshakeDropsSetCookie(t *testing.T) {
	var releases atomic.Int32
	base := startProxy(t, startUpstream(t), &releases)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, strings.Replace(base, "http", "ws", 1)+"/p/wscookie", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Header.Get("Set-Cookie") != "" || resp.Header.Get("X-Upstream") != "yes" {
		t.Errorf("handshake headers: %v", resp.Header)
	}
	if err := conn.Write(ctx, websocket.MessageText, []byte("ping")); err != nil {
		t.Fatal(err)
	}
	if _, data, err := conn.Read(ctx); err != nil || string(data) != "ping" {
		t.Fatalf("read %q %v", data, err)
	}
	conn.Close(websocket.StatusNormalClosure, "")
	waitFor(t, "release after websocket close", func() bool { return releases.Load() == 1 })
}

// An extension that does not complete the handshake gets an ordinary,
// sanitized HTTP answer relayed (or 502 if it gave none), and the client
// connection is closed instead of becoming a raw channel to the extension.
func TestWebSocketUpstreamRefusesUpgrade(t *testing.T) {
	cases := []struct {
		name, path string
		status     int
		body       string
	}{
		{"forbidden", "/wsreject?code=403", 403, "nope\n"},
		{"bad request", "/wsreject?code=400", 400, "nope\n"},
		{"no answer", "/wsraw?mode=drop", 502, ""},
		{"other protocol", "/wsraw?mode=other", 502, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var releases atomic.Int32
			base := startProxy(t, startUpstream(t), &releases)
			conn := dialProxy(t, base)
			_, _ = fmt.Fprintf(conn, "GET /p%s HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\n%s\r\n", tc.path, wsHandshakeHeaders)
			br := bufio.NewReader(conn)
			resp, err := http.ReadResponse(br, nil)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != tc.status {
				t.Errorf("status %d, want %d", resp.StatusCode, tc.status)
			}
			if tc.status == http.StatusBadGateway {
				var e struct{ Error struct{ Code string } }
				if err := json.Unmarshal(body, &e); err != nil || e.Error.Code != "backend_unavailable" {
					t.Errorf("body %q", body)
				}
			} else if string(body) != tc.body {
				t.Errorf("body %q, want %q", body, tc.body)
			}
			if resp.Header.Get("Set-Cookie") != "" {
				t.Errorf("Set-Cookie reached the client: %v", resp.Header)
			}
			if !resp.Close {
				t.Error("connection must be closed after a refused upgrade")
			}
			assertClosed(t, conn, br)
			waitFor(t, "release", func() bool { return releases.Load() == 1 })
		})
	}
}

// Hop-by-hop headers (including the ones named in Connection), Expect and a
// spoofed request id stay at the proxy on the WebSocket path too.
func TestWebSocketDropsHopByHopHeaders(t *testing.T) {
	var releases atomic.Int32
	base := startProxy(t, startUpstream(t), &releases)
	conn := dialProxy(t, base)
	_, _ = fmt.Fprintf(conn, "GET /p/wsheaders HTTP/1.1\r\nHost: x\r\nConnection: keep-alive, Upgrade, X-Hop\r\n"+
		"X-Hop: secret\r\nProxy-Authorization: Basic eDp5\r\nTe: trailers\r\nKeep-Alive: timeout=5\r\n"+
		"Expect: 100-continue\r\nX-Request-Id: spoofed\r\nX-Custom: keep\r\n%s\r\n", wsHandshakeHeaders)
	br := bufio.NewReader(conn)
	if resp := readFinalResponse(t, br); resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var got struct{ Header http.Header }
	if err := json.Unmarshal(readFrame(t, br), &got); err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"X-Hop", "Proxy-Authorization", "Te", "Keep-Alive", "Expect"} {
		if got.Header.Get(h) != "" {
			t.Errorf("%s reached the extension: %q", h, got.Header.Get(h))
		}
	}
	if vals := got.Header.Values(sdk.HeaderRequestID); len(vals) != 1 || vals[0] != "req-1" {
		t.Errorf("%s = %v, want [req-1]", sdk.HeaderRequestID, vals)
	}
	if got.Header.Get("X-Custom") != "keep" {
		t.Errorf("X-Custom = %q", got.Header.Get("X-Custom"))
	}
	_ = conn.Close()
	waitFor(t, "release after websocket close", func() bool { return releases.Load() == 1 })
}

// The extension ends the tunnel while the browser is idle: the frame it sent
// with the handshake still arrives, then the client connection is closed and
// release is called without waiting for the client to send anything.
func TestWebSocketExtensionHangsUp(t *testing.T) {
	var releases atomic.Int32
	base := startProxy(t, startUpstream(t), &releases)
	conn := dialProxy(t, base)
	_, _ = fmt.Fprintf(conn, "GET /p/wsraw?mode=hangup HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\n%s\r\n", wsHandshakeHeaders)
	br := bufio.NewReader(conn)
	if resp := readFinalResponse(t, br); resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if data := readFrame(t, br); string(data) != "bye" {
		t.Fatalf("frame %q", data)
	}
	assertClosed(t, conn, br)
	waitFor(t, "release after the extension hung up", func() bool { return releases.Load() == 1 })
}

// A missing port means the default port of the connection's scheme (plain http
// here), and the Origin must use that scheme.
func TestWebSocketOriginDefaultPorts(t *testing.T) {
	cases := []struct {
		origin, host string
		status       int
	}{
		{"http://example.test", "example.test:80", http.StatusSwitchingProtocols},
		{"http://example.test", "example.test", http.StatusSwitchingProtocols},
		{"http://Example.TEST:80", "example.test", http.StatusSwitchingProtocols},
		{"http://example.test:8080", "example.test:8080", http.StatusSwitchingProtocols},
		{"https://example.test", "example.test:443", http.StatusForbidden}, // a TLS origin for a plain connection
		{"https://example.test", "example.test", http.StatusForbidden},
		{"http://example.test", "example.test:8080", http.StatusForbidden},
		{"http://example.test:8080", "example.test", http.StatusForbidden},
		{"http://other.test", "example.test", http.StatusForbidden},
		{"ftp://example.test", "example.test", http.StatusForbidden},
		{"null", "example.test", http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.origin+" for "+tc.host, func(t *testing.T) {
			var releases atomic.Int32
			base := startProxy(t, startUpstream(t), &releases)
			conn := dialProxy(t, base)
			_, _ = fmt.Fprintf(conn, "GET /p/ws HTTP/1.1\r\nHost: %s\r\nOrigin: %s\r\nConnection: Upgrade\r\n%s\r\n", tc.host, tc.origin, wsHandshakeHeaders)
			resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != tc.status {
				t.Fatalf("status %d, want %d", resp.StatusCode, tc.status)
			}
			_ = conn.Close()
			waitFor(t, "release", func() bool { return releases.Load() == 1 })
		})
	}
}

func TestSameOrigin(t *testing.T) {
	cases := []struct {
		origin, host string
		tls          bool
		want         bool
	}{
		// plain connection
		{"http://example.test", "example.test:80", false, true},
		{"http://example.test", "example.test", false, true},
		{"http://Example.TEST:80", "example.test", false, true},
		{"http://example.test:8080", "example.test:8080", false, true},
		{"ws://example.test", "example.test", false, true},
		{"http://[::1]:8080", "[::1]:8080", false, true},
		{"https://example.test", "example.test", false, false}, // scheme of a TLS origin
		{"https://example.test", "example.test:443", false, false},
		{"wss://example.test", "example.test", false, false},
		{"http://example.test", "example.test:8080", false, false},
		{"http://example.test:8080", "example.test", false, false},
		{"http://other.test", "example.test", false, false},
		{"http://[::1]:8080", "[::1]:9090", false, false},
		// TLS connection
		{"https://example.test", "example.test:443", true, true},
		{"https://example.test", "example.test", true, true},
		{"https://Example.TEST:443", "example.test", true, true},
		{"https://example.test:8443", "example.test:8443", true, true},
		{"wss://example.test", "example.test", true, true},
		{"http://example.test", "example.test", true, false}, // downgraded scheme
		{"http://example.test:443", "example.test:443", true, false},
		{"ws://example.test", "example.test", true, false},
		{"https://example.test", "example.test:80", true, false},
		{"https://example.test:8443", "example.test", true, false},
		// never an origin
		{"", "example.test", false, false},
		{"null", "example.test", false, false},
		{"ftp://example.test", "example.test", false, false},
		{"file:///etc/passwd", "example.test", false, false},
		{"chrome-extension://abcdef", "example.test", false, false},
		{"//example.test", "example.test", false, false},
		{"example.test", "example.test", false, false},
		{"http://user@example.test", "example.test", false, false},
		{"http://example.test/path", "example.test", false, false},
		{"http://example.test/", "example.test", false, false},
		{"http://example.test?x=1", "example.test", false, false},
		{"http://example.test#x", "example.test", false, false},
		{"http://exa mple.test", "exa mple.test", false, false},
		{"http://example.test", "", false, false},
	}
	for _, tc := range cases {
		if got := SameOrigin(tc.origin, tc.host, tc.tls); got != tc.want {
			t.Errorf("SameOrigin(%q, %q, tls=%v) = %v, want %v", tc.origin, tc.host, tc.tls, got, tc.want)
		}
	}
}
