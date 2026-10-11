package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"mime"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync"

	"github.com/gofiber/fiber/v3"

	"github.com/Pukka922/edelweiss/sdk"
)

const placeholderIndex = `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>edelweiss</title>` +
	`<script type="importmap">{"imports":{}}</script></head><body>` +
	`<p>edelweiss: the web shell has not been built yet (run <code>make build</code>).</p></body></html>`

var (
	importMapRe  = regexp.MustCompile(`(?s)<script type="importmap">(.*?)</script>`)
	scriptOpenRe = regexp.MustCompile(`(?is)<script\b([^>]*)>`)
	// quotedRe matches a quoted attribute value; an unterminated quote runs to the end.
	quotedRe = regexp.MustCompile(`"[^"]*(?:"|$)|'[^']*(?:'|$)`)
	// srcAttrRe matches an attribute named src: whitespace before it, so neither
	// data-src nor src= inside an unquoted value (title=a/src=b) counts.
	srcAttrRe = regexp.MustCompile(`(?i)\ssrc\s*=`)
)

// hasSrc reports whether the attributes of a <script> tag contain a src
// attribute, ignoring text inside quoted values of other attributes.
func hasSrc(attrs []byte) bool {
	return srcAttrRe.Match(quotedRe.ReplaceAll(attrs, []byte(" ")))
}

// shell serves the web shell. In dev mode index.html is re-read on every
// request for it, so `vite build --watch` output is picked up without a restart.
type shell struct {
	fsys   fs.FS
	dev    bool
	logger *slog.Logger

	mu    sync.RWMutex
	index []byte
	csp   string
}

func newShell(fsys fs.FS, dev bool, logger *slog.Logger) (*shell, error) {
	sh := &shell{fsys: fsys, dev: dev, logger: logger}
	if err := sh.load(); err != nil {
		return nil, err
	}
	return sh, nil
}

func (sh *shell) load() error {
	index, err := fs.ReadFile(sh.fsys, "index.html")
	if err != nil {
		index = []byte(placeholderIndex)
	}
	csp, err := cspFor(index)
	if err != nil {
		return err
	}
	sh.mu.Lock()
	sh.index, sh.csp = index, csp
	sh.mu.Unlock()
	return nil
}

func (sh *shell) current() ([]byte, string) {
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	return sh.index, sh.csp
}

// cspFor builds the Content-Security-Policy for index.html: scripts only from
// the own origin, plus the single inline import map identified by its hash.
// Any other inline script would be blocked by that policy, so it is refused here.
func cspFor(index []byte) (string, error) {
	maps := importMapRe.FindAllSubmatch(index, -1)
	inline := 0
	for _, m := range scriptOpenRe.FindAllSubmatch(index, -1) {
		if !hasSrc(m[1]) {
			inline++
		}
	}
	switch {
	case len(maps) > 1:
		return "", errors.New("shell index.html: more than one import map")
	case len(maps) == 0 && bytes.Contains(index, []byte("importmap")):
		return "", errors.New(`shell index.html: "importmap" found, but not as a plain <script type="importmap"> the CSP can hash`)
	case inline > len(maps):
		return "", fmt.Errorf("shell index.html: %d inline script(s) besides the import map; the CSP allows only the import map", inline-len(maps))
	}
	scriptSrc := "'self'"
	if len(maps) == 1 {
		// Browsers hash the script text after newline normalisation (CRLF and CR become LF).
		text := bytes.ReplaceAll(bytes.ReplaceAll(maps[0][1], []byte("\r\n"), []byte("\n")), []byte("\r"), []byte("\n"))
		sum := sha256.Sum256(text)
		scriptSrc += " 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	}
	return "default-src 'self'; script-src " + scriptSrc + "; style-src 'self' 'unsafe-inline'; img-src 'self' data:; " +
		"connect-src 'self'; font-src 'self'; object-src 'none'; base-uri 'self'; frame-ancestors 'none'; form-action 'self'", nil
}

// cspHeader sets the current shell CSP on every response.
func (sh *shell) cspHeader(c fiber.Ctx) error {
	_, csp := sh.current()
	c.Set(fiber.HeaderContentSecurityPolicy, csp)
	return c.Next()
}

func (sh *shell) handler(c fiber.Ctx) error {
	// Fiber does not percent-decode wildcard params (Config.UnescapePath is
	// false by default), so decode here, then clean the result so encoded
	// traversal (..%2f, %2e%2e) cannot leave the FS root.
	raw, err := url.PathUnescape(c.Params("*"))
	if err != nil {
		return fiber.ErrNotFound
	}
	p := strings.TrimPrefix(path.Clean("/"+raw), "/")
	if p != "" && p != "index.html" {
		if fs.ValidPath(p) {
			if data, err := fs.ReadFile(sh.fsys, p); err == nil {
				c.Set(fiber.HeaderContentType, contentType(p))
				c.Set(fiber.HeaderCacheControl, sh.cacheControl(p))
				return c.Send(data)
			}
		}
		if path.Ext(p) != "" {
			return fiber.ErrNotFound
		}
	}
	if sh.dev {
		if err := sh.load(); err != nil {
			sh.logger.Error("reloading the shell's index.html failed", "err", err)
			return sdk.NewError(fiber.StatusInternalServerError, "shell_invalid", err.Error())
		}
	}
	index, csp := sh.current()
	c.Set(fiber.HeaderContentSecurityPolicy, csp)
	c.Set(fiber.HeaderContentType, "text/html; charset=utf-8")
	c.Set(fiber.HeaderCacheControl, "no-cache")
	return c.Send(index)
}

// cacheControl: hashed assets and versioned vendor files are immutable, except
// in dev mode where watch builds may rewrite them in place.
func (sh *shell) cacheControl(p string) string {
	if !sh.dev && (strings.HasPrefix(p, "assets/") || strings.HasPrefix(p, "vendor/")) {
		return "public, max-age=31536000, immutable"
	}
	return "no-cache"
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
	case ".html":
		return "text/html; charset=utf-8"
	}
	if t := mime.TypeByExtension(path.Ext(name)); t != "" {
		return t
	}
	return "application/octet-stream"
}
