package sdk

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"mime"
	"net/url"
	"path"
	"strings"

	"github.com/gofiber/fiber/v3"
)

// uiHandler serves files of fsys below UIPrefix with ETag revalidation.
func uiHandler(fsys fs.FS) fiber.Handler {
	return func(c fiber.Ctx) error {
		// Fiber does not percent-decode wildcard params (Config.UnescapePath is
		// false by default), so decode here, then clean and validate the result
		// so encoded traversal (..%2f, %2e%2e) cannot escape the FS root.
		raw, err := url.PathUnescape(c.Params("*"))
		if err != nil {
			return fiber.ErrNotFound
		}
		name := strings.TrimPrefix(path.Clean("/"+raw), "/")
		if name == "" || !fs.ValidPath(name) {
			return fiber.ErrNotFound
		}
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return fiber.ErrNotFound
		}
		sum := sha256.Sum256(data)
		etag := `"` + hex.EncodeToString(sum[:16]) + `"`
		c.Set(fiber.HeaderETag, etag)
		c.Set(fiber.HeaderCacheControl, "no-cache")
		if c.Get(fiber.HeaderIfNoneMatch) == etag {
			return c.SendStatus(fiber.StatusNotModified)
		}
		c.Set(fiber.HeaderContentType, contentType(name))
		return c.Send(data)
	}
}

func contentType(name string) string {
	switch path.Ext(name) {
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".json":
		return "application/json; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	}
	if t := mime.TypeByExtension(path.Ext(name)); t != "" {
		return t
	}
	return "application/octet-stream"
}
