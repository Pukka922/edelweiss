package sdk

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
)

type envelope struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func decodeEnvelope(t *testing.T, body io.Reader) envelope {
	t.Helper()
	var e envelope
	if err := json.NewDecoder(body).Decode(&e); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return e
}

func TestErrorHandler(t *testing.T) {
	app := fiber.New(fiber.Config{ErrorHandler: ErrorHandler})
	app.Get("/api", func(c fiber.Ctx) error { return NewError(422, "invalid_hostname", "bad host") })
	app.Get("/fiber", func(c fiber.Ctx) error { return fiber.ErrForbidden })
	app.Get("/plain", func(c fiber.Ctx) error { return errors.New("boom") })

	cases := []struct {
		path   string
		status int
		code   string
	}{
		{"/api", 422, "invalid_hostname"},
		{"/fiber", 403, "forbidden"},
		{"/plain", 500, "internal_error"},
		{"/missing", 404, "not_found"},
	}
	for _, tc := range cases {
		resp, err := app.Test(httptest.NewRequest("GET", tc.path, nil))
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != tc.status {
			t.Errorf("%s: status %d, want %d", tc.path, resp.StatusCode, tc.status)
		}
		if e := decodeEnvelope(t, resp.Body); e.Error.Code != tc.code {
			t.Errorf("%s: code %q, want %q", tc.path, e.Error.Code, tc.code)
		}
	}
}

// "unauthenticated" is the core's code for a lost session: the web shell answers it with the login page. An
// extension that answers 401 in the plain fiber way must not get that code, or it would sign the user out.
func TestBareUnauthorizedIsNotTheCoresSessionCode(t *testing.T) {
	app := fiber.New(fiber.Config{ErrorHandler: ErrorHandler})
	app.Get("/bare", func(c fiber.Ctx) error { return fiber.ErrUnauthorized })
	app.Get("/custom", func(c fiber.Ctx) error { return fiber.NewError(fiber.StatusUnauthorized, "token expired") })

	for _, path := range []string{"/bare", "/custom"} {
		resp, err := app.Test(httptest.NewRequest("GET", path, nil))
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 401 {
			t.Errorf("%s: status %d, want 401", path, resp.StatusCode)
		}
		if e := decodeEnvelope(t, resp.Body); e.Error.Code != "unauthorized" {
			t.Errorf("%s: code %q, want %q", path, e.Error.Code, "unauthorized")
		}
	}
}

// Server-side failures reach the log whatever form the error takes; client
// errors do not, and neither do the backend_* answers about extensions whose
// cause edelweissd logs where it happens (once, not per request).
func TestServerErrorsAreLogged(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	app := fiber.New(fiber.Config{ErrorHandler: ErrorHandler})
	app.Get("/api5xx", func(c fiber.Ctx) error { return NewError(503, "backend_down", "try later") })
	app.Get("/fiber5xx", func(c fiber.Ctx) error { return fiber.ErrBadGateway })
	app.Get("/plain", func(c fiber.Ctx) error { return errors.New("boom") })
	app.Get("/api4xx", func(c fiber.Ctx) error { return NewError(422, "invalid_hostname", "bad host") })
	app.Get("/fiber4xx", func(c fiber.Ctx) error { return fiber.ErrForbidden })
	app.Get("/unavailable", func(c fiber.Ctx) error {
		return NewError(503, "backend_unavailable", "extension could not be started")
	})
	app.Get("/unreachable", func(c fiber.Ctx) error { return NewError(502, "backend_unavailable", "extension is not reachable") })
	app.Get("/crashloop", func(c fiber.Ctx) error { return NewError(503, "backend_crashloop", "extension is crashing") })

	for _, p := range []string{"/api5xx", "/fiber5xx", "/plain", "/api4xx", "/fiber4xx", "/unavailable", "/unreachable", "/crashloop"} {
		if _, err := app.Test(httptest.NewRequest("GET", p, nil)); err != nil {
			t.Fatal(err)
		}
	}
	out := logs.String()
	for _, want := range []string{"method=GET path=/api5xx", "method=GET path=/fiber5xx", "method=GET path=/plain"} {
		if !strings.Contains(out, want) || !strings.Contains(out, "level=ERROR") {
			t.Errorf("no error logged with %q:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"/api4xx", "/fiber4xx", "/unavailable", "/unreachable", "/crashloop"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("%s logged:\n%s", unwanted, out)
		}
	}
}

func TestPlainErrorDoesNotLeakMessage(t *testing.T) {
	app := fiber.New(fiber.Config{ErrorHandler: ErrorHandler})
	app.Get("/plain", func(c fiber.Ctx) error { return errors.New("secret detail") })
	resp, _ := app.Test(httptest.NewRequest("GET", "/plain", nil))
	if e := decodeEnvelope(t, resp.Body); e.Error.Message == "secret detail" {
		t.Fatal("internal error message leaked to client")
	}
}

func TestErrorDetails(t *testing.T) {
	app := fiber.New(fiber.Config{ErrorHandler: ErrorHandler})
	app.Get("/", func(c fiber.Ctx) error {
		return NewError(422, "missing_packages", "missing packages: docker").
			WithDetails(map[string]any{"packages": []string{"docker"}, "code": "ignored", "message": "ignored"})
	})
	resp, err := app.Test(httptest.NewRequest("GET", "/", nil))
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Error map[string]any `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 422 || body.Error["code"] != "missing_packages" || body.Error["message"] != "missing packages: docker" {
		t.Fatalf("answer %d %v", resp.StatusCode, body.Error)
	}
	pkgs, _ := body.Error["packages"].([]any)
	if len(pkgs) != 1 || pkgs[0] != "docker" {
		t.Fatalf("packages = %v", body.Error["packages"])
	}
	if got := NewError(422, "x", "y").Error(); got != "422 x: y" {
		t.Fatalf("Error() = %q", got)
	}
}
