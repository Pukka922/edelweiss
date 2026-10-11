package proxy

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/valyala/fasthttp"

	"github.com/Pukka922/edelweiss/sdk"
)

// handshakeTimeout bounds how long the extension may take to answer the upgrade.
const handshakeTimeout = 10 * time.Second

// IsUpgrade reports whether the request is a WebSocket upgrade.
func IsUpgrade(c fiber.Ctx) bool {
	if !strings.EqualFold(c.Get(fiber.HeaderUpgrade), "websocket") {
		return false
	}
	for _, tok := range strings.Split(c.Get(fiber.HeaderConnection), ",") {
		if strings.EqualFold(strings.TrimSpace(tok), "upgrade") {
			return true
		}
	}
	return false
}

// SameOrigin reports whether origin (a browser's Origin header) names host (the
// Host header of the request) on a connection with or without TLS. The origin
// must be a plain web origin whose scheme is the connection's (http or ws
// without TLS, https or wss with it); a missing port is that scheme's default
// port, so with TLS https://x matches both x and x:443.
func SameOrigin(origin, host string, tls bool) bool {
	o, err := url.Parse(origin)
	if err != nil || o.Host == "" || o.User != nil || o.Path != "" || o.RawQuery != "" || o.Fragment != "" || o.Opaque != "" {
		return false
	}
	def, schemes := "80", [2]string{"http", "ws"}
	if tls {
		def, schemes = "443", [2]string{"https", "wss"}
	}
	if s := strings.ToLower(o.Scheme); s != schemes[0] && s != schemes[1] {
		return false
	}
	h, err := url.Parse("//" + host)
	if err != nil || h.Host != host || h.Hostname() == "" || h.User != nil || h.Path != "" {
		return false
	}
	op, hp := o.Port(), h.Port()
	if op == "" {
		op = def
	}
	if hp == "" {
		hp = def
	}
	return strings.EqualFold(o.Hostname(), h.Hostname()) && op == hp
}

// ForwardWebSocket replays the (sanitized) upgrade request to the extension.
// Only once the extension has accepted it is the client connection hijacked
// and piped raw; any other answer is relayed as an ordinary response, and the
// client connection is closed instead of becoming a channel to the extension.
func ForwardWebSocket(c fiber.Ctx, up Upstream, target string, id Identity, release func()) error {
	// Until the tunnel exists, every answer ends the client connection: it was
	// offered for an upgrade, not for another request.
	fail := func(err error) error {
		release()
		c.Response().SetConnectionClose()
		return err
	}
	if origin := c.Get(fiber.HeaderOrigin); origin != "" && !SameOrigin(origin, string(c.Request().Host()), c.RequestCtx().IsTLS()) {
		return fail(sdk.NewError(fiber.StatusForbidden, "origin_mismatch", "cross-origin WebSocket rejected"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	conn, err := up.Dial(ctx)
	cancel()
	if err != nil {
		return fail(unavailable())
	}

	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)
	c.Request().CopyTo(req)
	in := clientHeaders(c)
	named := connectionNamed(in["Connection"])
	for canon := range in {
		if canon != fiber.HeaderHost && dropRequestHeader(canon, named) {
			req.Header.Del(canon)
		}
	}
	req.Header.Del(fiber.HeaderCookie) // fasthttp keeps cookies apart from the other headers
	req.Header.Del(fiber.HeaderAuthorization)
	req.Header.Set(fiber.HeaderConnection, "Upgrade")
	req.Header.Set(fiber.HeaderUpgrade, "websocket")
	req.SetRequestURI(target)
	req.Header.SetHost("edelweiss-ext")
	setIdentity(req.Header.Set, id)

	_ = conn.SetDeadline(time.Now().Add(handshakeTimeout))
	upr := bufio.NewReader(conn) // may read past the handshake: frames sent along with it
	var resp *http.Response
	if _, err = req.WriteTo(conn); err == nil {
		resp, err = http.ReadResponse(upr, &http.Request{Method: http.MethodGet})
	}
	if err != nil {
		_ = conn.Close()
		return fail(unavailable())
	}
	if resp.StatusCode != http.StatusSwitchingProtocols || !strings.EqualFold(resp.Header.Get("Upgrade"), "websocket") {
		err := refused(c, resp)
		_ = conn.Close()
		release()
		return err
	}
	_ = conn.SetDeadline(time.Time{})
	head := switchingProtocols(resp.Header)

	rc := c.RequestCtx()
	rc.HijackSetNoResponse(true)
	rc.Hijack(func(client net.Conn) {
		defer release()
		if _, err := client.Write(head); err != nil {
			_ = conn.Close()
			return
		}
		pipe(client, conn, upr)
	})
	return nil
}

// refused relays an extension's answer that is not a WebSocket handshake as an
// ordinary response, sanitized like Forward's and with a bounded body (an
// unreadable answer becomes 502). The connection is closed after it.
func refused(c fiber.Ctx, resp *http.Response) error {
	c.Response().SetConnectionClose()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBuffered+1))
	resp.Body.Close()
	if err != nil || len(data) > maxBuffered || resp.StatusCode < http.StatusOK {
		return unavailable() // includes a 101 to another protocol
	}
	c.Status(resp.StatusCode)
	copyResponseHeaders(c, resp.Header)
	return c.Send(data)
}

// switchingProtocols renders the extension's 101 answer for the client. The
// handshake headers (Upgrade, Connection, Sec-WebSocket-*) and other end-to-end
// headers are kept; cookies and hop-by-hop headers are not.
func switchingProtocols(h http.Header) []byte {
	out := http.Header{}
	named := connectionNamed(h["Connection"])
	for name, values := range h {
		if !dropResponseHeader(name, named) {
			out[name] = values
		}
	}
	out.Set(fiber.HeaderUpgrade, "websocket")
	out.Set(fiber.HeaderConnection, "Upgrade")
	var b bytes.Buffer
	b.WriteString("HTTP/1.1 101 Switching Protocols\r\n")
	_ = out.Write(&b)
	b.WriteString("\r\n")
	return b.Bytes()
}

// pipe copies between the hijacked client connection and the extension until
// either side is done, then stops the other direction too. upr is the
// extension's side, including bytes buffered while reading the handshake.
func pipe(client, conn net.Conn, upr io.Reader) {
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(client, upr); done <- struct{}{} }()
	go func() { _, _ = io.Copy(conn, client); done <- struct{}{} }()
	<-done
	_ = conn.Close()
	// fasthttp's hijacked conn ignores Close (it closes the connection itself
	// once the hijack handler returns); a past deadline releases a copy that is
	// blocked on an idle client.
	_ = client.SetDeadline(time.Now())
	_ = client.Close()
	<-done
}
