package sdk

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gofiber/fiber/v3"
)

func TestUserFrom(t *testing.T) {
	app := fiber.New(fiber.Config{ErrorHandler: ErrorHandler})
	app.Get("/me", func(c fiber.Ctx) error { return c.JSON(UserFrom(c)) })

	req := httptest.NewRequest("GET", "/me", nil)
	req.Header.Set(HeaderUser, "myUser")
	req.Header.Set(HeaderUID, "1000")
	req.Header.Set(HeaderAdmin, "1")
	req.Header.Set(HeaderLang, "de")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	var u User
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		t.Fatal(err)
	}
	if u != (User{Name: "myUser", UID: 1000, Admin: true, Lang: "de"}) {
		t.Fatalf("unexpected user %+v", u)
	}
}

// fasthttp reuses the header buffers of a request for later ones: a User kept
// beyond its handler (a goroutine, a stream) must not change with them.
func TestUserFromSurvivesTheRequest(t *testing.T) {
	app := fiber.New()
	var captured []User
	app.Get("/me", func(c fiber.Ctx) error {
		captured = append(captured, UserFrom(c))
		return c.SendStatus(fiber.StatusNoContent)
	})
	want := func(i int) User {
		// the same lengths for every request, so later ones overwrite the same bytes
		return User{Name: fmt.Sprintf("user%03d", i), UID: 1000 + i, Lang: fmt.Sprintf("l%d", i%10)}
	}
	for i := range 30 {
		u := want(i)
		req := httptest.NewRequest("GET", "/me", nil)
		req.Header.Set(HeaderUser, u.Name)
		req.Header.Set(HeaderUID, strconv.Itoa(u.UID))
		req.Header.Set(HeaderLang, u.Lang)
		if _, err := app.Test(req); err != nil {
			t.Fatal(err)
		}
	}
	for i, got := range captured {
		if got != want(i) {
			t.Fatalf("user captured by request %d changed with later requests: %+v, want %+v", i, got, want(i))
		}
	}
}

func TestUserFromDefaults(t *testing.T) {
	app := fiber.New()
	app.Get("/me", func(c fiber.Ctx) error { return c.JSON(UserFrom(c)) })
	resp, _ := app.Test(httptest.NewRequest("GET", "/me", nil))
	var u User
	_ = json.NewDecoder(resp.Body).Decode(&u)
	if u.UID != -1 || u.Admin || u.Lang != "en" {
		t.Fatalf("unexpected defaults %+v", u)
	}
}

func TestRequireAdmin(t *testing.T) {
	app := fiber.New(fiber.Config{ErrorHandler: ErrorHandler})
	app.Put("/x", RequireAdmin(), func(c fiber.Ctx) error { return c.SendStatus(204) })

	req := httptest.NewRequest("PUT", "/x", nil)
	resp, _ := app.Test(req)
	if resp.StatusCode != 403 {
		t.Fatalf("non-admin: status %d", resp.StatusCode)
	}
	if e := decodeEnvelope(t, resp.Body); e.Error.Code != "admin_required" {
		t.Fatalf("code %q", e.Error.Code)
	}

	req = httptest.NewRequest("PUT", "/x", nil)
	req.Header.Set(HeaderAdmin, "1")
	resp, _ = app.Test(req)
	if resp.StatusCode != 204 {
		t.Fatalf("admin: status %d", resp.StatusCode)
	}
}
