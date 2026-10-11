// Package proxy forwards requests from edelweissd to extension processes.
package proxy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/Pukka922/edelweiss/sdk"
)

// Upstream is an extension process reachable over a Unix socket.
type Upstream interface {
	Transport() http.RoundTripper
	Dial(ctx context.Context) (net.Conn, error)
}

// Identity is asserted to the extension via X-Edelweiss-* headers.
type Identity struct {
	User      string
	UID       int
	Admin     bool
	Lang      string
	RequestID string
}

// maxBuffered is the largest response that is buffered instead of streamed.
const maxBuffered = 1 << 20

var hopByHop = map[string]bool{
	"Connection": true, "Keep-Alive": true, "Proxy-Authenticate": true, "Proxy-Authorization": true,
	"Proxy-Connection": true, "Te": true, "Trailer": true, "Transfer-Encoding": true, "Upgrade": true,
}

// requestStripped are client headers never forwarded to extensions. Expect is
// answered by edelweissd itself (fasthttp sends the 100 Continue). Forwarding
// headers would describe proxies that edelweissd does not have: whatever a
// client puts there (its address, host or scheme) is a claim, not a fact.
var requestStripped = map[string]bool{
	"Cookie": true, "Authorization": true, "X-Csrf-Token": true, "Host": true, "Expect": true,
	"Forwarded": true, "X-Forwarded-For": true, "X-Forwarded-Host": true, "X-Forwarded-Proto": true, "X-Real-Ip": true,
}

func isEdelweissHeader(name string) bool {
	return strings.HasPrefix(http.CanonicalHeaderKey(name), "X-Edelweiss-")
}

// connectionNamed returns the canonical names listed in Connection header
// values: those headers are hop-by-hop as well (RFC 9110 §7.6.1).
func connectionNamed(values []string) map[string]bool {
	named := map[string]bool{}
	for _, v := range values {
		for _, tok := range strings.Split(v, ",") {
			if tok = strings.TrimSpace(tok); tok != "" {
				named[http.CanonicalHeaderKey(tok)] = true
			}
		}
	}
	return named
}

// clientHeaders returns the client's request headers by canonical name.
func clientHeaders(c fiber.Ctx) http.Header {
	h := http.Header{}
	for name, values := range c.GetReqHeaders() {
		canon := http.CanonicalHeaderKey(name)
		h[canon] = append(h[canon], values...)
	}
	return h
}

// dropRequestHeader reports whether a client request header must stay at the
// proxy: credentials, hop-by-hop headers (named lists the ones the client
// declared in Connection), Expect, and anything the identity headers assert.
func dropRequestHeader(canon string, named map[string]bool) bool {
	return hopByHop[canon] || requestStripped[canon] || named[canon] ||
		isEdelweissHeader(canon) || canon == sdk.HeaderRequestID
}

// dropResponseHeader reports whether an extension response header must stay at
// the proxy: hop-by-hop headers, the framing (set by fasthttp) and cookies –
// extensions never set cookies on the edelweiss origin.
func dropResponseHeader(canon string, named map[string]bool) bool {
	return hopByHop[canon] || named[canon] || canon == "Content-Length" ||
		canon == "Set-Cookie" || canon == "Set-Cookie2"
}

// copyResponseHeaders copies the extension's response headers that may reach
// the client.
func copyResponseHeaders(c fiber.Ctx, h http.Header) {
	named := connectionNamed(h["Connection"])
	for name, values := range h {
		if dropResponseHeader(name, named) {
			continue
		}
		for _, v := range values {
			c.Response().Header.Add(name, v)
		}
	}
}

func setIdentity(set func(k, v string), id Identity) {
	set(sdk.HeaderUser, id.User)
	set(sdk.HeaderUID, strconv.Itoa(id.UID))
	if id.Admin {
		set(sdk.HeaderAdmin, "1")
	} else {
		set(sdk.HeaderAdmin, "0")
	}
	set(sdk.HeaderLang, id.Lang)
	set(sdk.HeaderRequestID, id.RequestID)
}

func unavailable() error {
	return sdk.NewError(fiber.StatusBadGateway, "backend_unavailable", "extension is not reachable")
}

// bodyGrace bounds how long Forward waits for the transport to let go of the
// client's request body once the response is complete.
const bodyGrace = time.Second

var errBodyDetached = errors.New("proxy: request body no longer available")

// requestBody hands the client's request body to the transport. fasthttp
// recycles the body (a stream's buffers or the in-memory slice) once the
// response is written, but net/http's transport reads it from its own
// write-loop goroutine: it always performs one final read after the declared
// length, and it keeps pumping the body after an upstream that answered early.
// Reading recycled memory is a data race (and fasthttp panics on a released
// stream), so Forward calls detach before it lets fasthttp reclaim the body.
//
// The server's read timeout covers a whole request including its body, which
// would cut off every upload that takes longer. For a streamed body, the client
// connection's read deadline is therefore moved one window ahead whenever the
// transport asks for more data and again after every read that made progress:
// an upload is bounded by the client's pauses, not by its duration, and not by
// an extension that stops reading for a while either (the deadline would
// otherwise expire while the client's data waits in the buffers).
type requestBody struct {
	mu       sync.Mutex // held for the duration of every Read
	src      io.Reader
	detached bool
	eof      atomic.Bool   // src was read to its end
	closed   chan struct{} // closed when the transport closes the body
	once     sync.Once

	setDeadline func(time.Time) // the client connection's read deadline
	window      time.Duration   // > 0: extend the deadline by this around every read
	deadlineMu  sync.Mutex      // orders extend against cutLoose
	cut         bool            // cutLoose was called: never extend again
}

func newRequestBody(src io.Reader, setDeadline func(time.Time), window time.Duration) *requestBody {
	return &requestBody{src: src, closed: make(chan struct{}), setDeadline: setDeadline, window: window}
}

func (b *requestBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.detached {
		return 0, errBodyDetached
	}
	b.extend() // the client has a full window from the moment data is wanted
	n, err := b.src.Read(p)
	if err == io.EOF {
		b.eof.Store(true)
	}
	if n > 0 {
		b.extend()
	}
	return n, err
}

// extend moves the read deadline window ahead, unless the exchange has been
// cut loose: once cutLoose ran, no read (starting or completing) moves it again.
func (b *requestBody) extend() {
	if b.window <= 0 {
		return
	}
	b.deadlineMu.Lock()
	defer b.deadlineMu.Unlock()
	if !b.cut {
		b.setDeadline(time.Now().Add(b.window))
	}
}

// cutLoose unblocks a read that is stuck on the client: the read deadline is
// moved to now, and no read that completes later may move it again.
func (b *requestBody) cutLoose() {
	b.deadlineMu.Lock()
	defer b.deadlineMu.Unlock()
	b.cut = true
	b.setDeadline(time.Now())
}

func (b *requestBody) Close() error {
	b.once.Do(func() { close(b.closed) })
	return nil
}

// complete reports whether the transport has read the whole body so far,
// without waiting for a read in progress. Only a body the upstream had not
// read to the end before it answered can be incomplete here.
func (b *requestBody) complete() bool {
	return b == nil || b.eof.Load()
}

// detach waits for the transport to finish with the body and then guarantees
// that no further read reaches src. If the transport is still busy after
// bodyGrace (upstream answered before consuming the upload), the body is cut
// loose to unblock a read that is stuck on the client connection. It reports
// whether the whole body was consumed; false means the upload was cut short and
// the client connection must not be reused.
func (b *requestBody) detach() (consumed bool) {
	if b == nil {
		return true
	}
	aborted := false
	cut := func() {
		if !aborted {
			aborted = true
			b.cutLoose()
		}
	}
	timer := time.NewTimer(bodyGrace)
	defer timer.Stop()
	select {
	case <-b.closed:
	case <-timer.C:
		cut()
	}
	// Taking the lock waits for a read that is still in flight; abort releases
	// one that is stuck on the client, so this cannot wait for the client forever.
	if b.mu.TryLock() {
		b.detached = true
		b.mu.Unlock()
	} else {
		locked := make(chan struct{})
		go func() {
			b.mu.Lock()
			b.detached = true
			b.mu.Unlock()
			close(locked)
		}()
		timer.Reset(bodyGrace)
		select {
		case <-locked:
		case <-timer.C:
			cut()
			<-locked
		}
	}
	return b.eof.Load() && !aborted
}

// responseStream is the extension's response body as fasthttp sends it to the
// client. fasthttp consumes it on the connection's own server goroutine while
// it writes the response – writeBodyChunked frames every Write as a chunk and
// flushes it – and calls Close on that goroutine once the body is done
// (written, failed or abandoned by the client), before it recycles the
// RequestCtx and the request body. Ending the exchange in Close therefore
// happens while the request still owns its context; a SendStreamWriter
// goroutine is neither awaited nor stopped by fasthttp and outlives it.
type responseStream struct {
	body io.ReadCloser
	end  func()
	err  error // first read error other than io.EOF
	once sync.Once
}

// SupportsBodyWriteTo lets fasthttp use WriteTo for the chunked framing
// (fasthttp.BodyWriterTo).
func (s *responseStream) SupportsBodyWriteTo() bool { return true }

// WriteTo relays the body; every Write is one flushed chunk, so events reach
// the client as soon as the extension sends them. A body that ends with an
// error (the extension died or cut it short) returns that error: fasthttp then
// omits the terminating chunk and drops the connection, so the client sees the
// truncation instead of a complete response.
func (s *responseStream) WriteTo(w io.Writer) (int64, error) {
	buf := make([]byte, 32<<10)
	var total int64
	for {
		n, err := s.Read(buf)
		if n > 0 {
			m, werr := w.Write(buf[:n])
			total += int64(m)
			if werr != nil {
				return total, werr // the client went away
			}
		}
		if err == io.EOF {
			return total, nil
		}
		if err != nil {
			return total, err
		}
	}
}

// Read is the plain reader view of the body: a failure is reported after the
// data that came with it, never turned into a clean end of body.
func (s *responseStream) Read(p []byte) (int, error) {
	if s.err != nil {
		return 0, s.err
	}
	n, err := s.body.Read(p)
	if err != nil && err != io.EOF {
		s.err = err
		if n > 0 {
			err = nil
		}
	}
	return n, err
}

// Close ends the exchange, exactly once: it aborts the upstream response if it
// was not read to its end, waits until the transport has let go of the request
// body, and calls release.
func (s *responseStream) Close() error {
	s.once.Do(func() {
		_ = s.body.Close()
		s.end()
	})
	return nil
}

// Forward proxies a plain HTTP request. Small responses are buffered, large or
// unknown-length responses (SSE, downloads) are streamed with a flush per chunk.
func Forward(c fiber.Ctx, up Upstream, target string, id Identity, release func()) error {
	var (
		body   io.Reader = http.NoBody
		guard  *requestBody
		length int64 // <= 0: unknown, sent chunked
	)
	// The read deadline is managed on the connection's server goroutine and
	// the transport's write loop – after fiber has recycled c – so only the
	// fasthttp context is captured, and it belongs to this request until the
	// exchange is finished.
	rc := c.RequestCtx()
	setDeadline := func(t time.Time) {
		if conn := rc.Conn(); conn != nil {
			_ = conn.SetReadDeadline(t)
		}
	}
	// With StreamRequestBody fasthttp exposes every body (even an empty one) as
	// a stream, so an explicit Content-Length: 0 must be told apart here.
	if c.Request().IsBodyStream() {
		if cl := c.Request().Header.ContentLength(); cl != 0 {
			// the server's read timeout becomes the longest pause in the upload
			guard = newRequestBody(c.Request().BodyStream(), setDeadline, c.App().Server().ReadTimeout)
			length = int64(cl)
		}
	} else if b := c.Request().Body(); len(b) > 0 { // raw bytes, c.Body() would decode Content-Encoding
		guard = newRequestBody(bytes.NewReader(b), setDeadline, 0)
		length = int64(len(b))
	}
	if guard != nil {
		body = guard
	}
	// finish ends the exchange as far as the request body is concerned. It runs
	// on the connection's server goroutine – in this handler, or in
	// responseStream.Close while fasthttp writes the response. When it reports
	// that the upload was cut short, the rest of it is still on the connection,
	// which must therefore not carry another request.
	finish := guard.detach
	req, err := http.NewRequestWithContext(context.Background(), c.Method(), "http://edelweiss-ext"+target, body)
	if err != nil {
		if guard != nil {
			_ = guard.Close() // the transport never saw the body
		}
		if !finish() {
			c.Response().SetConnectionClose()
		}
		release()
		return sdk.NewError(fiber.StatusBadRequest, "bad_request", "invalid request")
	}
	if length > 0 {
		req.ContentLength = length
	}
	in := clientHeaders(c)
	named := connectionNamed(in["Connection"])
	for canon, values := range in {
		if !dropRequestHeader(canon, named) {
			req.Header[canon] = values
		}
	}
	setIdentity(req.Header.Set, id)

	resp, err := up.Transport().RoundTrip(req)
	if err != nil {
		// e.g. the upload stalled for longer than the read timeout
		if !finish() {
			c.Response().SetConnectionClose()
		}
		release()
		return unavailable()
	}
	c.Status(resp.StatusCode)
	copyResponseHeaders(c, resp.Header)

	head := c.Method() == fiber.MethodHead
	if head || (resp.ContentLength >= 0 && resp.ContentLength <= maxBuffered &&
		!strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream")) {
		data, err := io.ReadAll(io.LimitReader(resp.Body, maxBuffered+1))
		resp.Body.Close()
		consumed := finish()
		release()
		if err != nil {
			c.Response().Reset() // drop the status and headers copied from the cut-off response
		}
		if !consumed {
			c.Response().SetConnectionClose() // upload was cut short: do not reuse the connection
		}
		if err != nil {
			return unavailable()
		}
		if head {
			// No body and no body framing. fasthttp leaves the Content-Length of
			// a HEAD response alone, so the extension's length is what GET would
			// return (unknown length: none).
			if resp.ContentLength >= 0 {
				c.Response().Header.SetContentLength(int(resp.ContentLength))
			}
			return nil
		}
		return c.Send(data)
	}

	if !guard.complete() {
		// The extension answered before it had the whole upload and may never
		// read the rest. Unread upload bytes must not be parsed as the next
		// request, and the head – it goes out before the body – is the last
		// chance to announce that the connection ends with this response.
		c.Response().SetConnectionClose()
	}
	c.Response().ImmediateHeaderFlush = true // e.g. an SSE client sees the head before the first event
	c.Response().SetBodyStream(&responseStream{body: resp.Body, end: func() {
		finish()
		release()
	}}, -1)
	return nil
}
