package sdk

import (
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v3"
)

// User is the identity of the person using the extension, as asserted by edelweissd.
type User struct {
	Name  string `json:"name"`
	UID   int    `json:"uid"`
	Admin bool   `json:"admin"`
	Lang  string `json:"lang"`
}

// UserFrom returns the user of the current request. UID is -1 when unknown,
// Lang defaults to "en".
//
// The returned User is a copy and may be kept after the handler returns. c
// itself may not: fasthttp reuses the request's buffers (and the strings that
// c.Get, c.Params or c.Query return point into them) for the next request.
// Code that outlives the handler, such as a goroutine or an SSE stream, must
// capture what it needs from c first and never touch c afterwards.
func UserFrom(c fiber.Ctx) User {
	uid, err := strconv.Atoi(c.Get(HeaderUID))
	if err != nil {
		uid = -1
	}
	lang := strings.Clone(c.Get(HeaderLang))
	if lang == "" {
		lang = "en"
	}
	return User{Name: strings.Clone(c.Get(HeaderUser)), UID: uid, Admin: c.Get(HeaderAdmin) == "1", Lang: lang}
}

// RequireAdmin rejects requests that are not made in admin mode.
func RequireAdmin() fiber.Handler {
	return func(c fiber.Ctx) error {
		if !UserFrom(c).Admin {
			return NewError(fiber.StatusForbidden, "admin_required", "administrative access required")
		}
		return c.Next()
	}
}
