package sdk

import (
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/sse"
)

// SSE returns a handler that streams Server-Sent Events. Each event is flushed
// immediately; a heartbeat comment keeps proxies from closing idle streams.
//
// handler runs while the response is being written, after the route's handler
// chain has returned, and it may still run after the client has gone and the
// request has been recycled. Read everything it needs from c at its start,
// before the first event – UserFrom(c) returns a copy, other values such as
// c.Params or c.Query must be copied with strings.Clone – and use only those
// copies and the stream in the event loop, never c.
func SSE(handler sse.Handler) fiber.Handler {
	return sse.New(sse.Config{Handler: handler})
}
