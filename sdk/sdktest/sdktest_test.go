package sdktest

import (
	"testing"

	"github.com/gofiber/fiber/v3"

	"github.com/Pukka922/edelweiss/sdk"
)

func TestRequestCarriesIdentity(t *testing.T) {
	app := fiber.New()
	app.Get("/me", func(c fiber.Ctx) error { return c.JSON(sdk.UserFrom(c)) })
	resp := Do(t, app, Request("GET", "/me", nil, sdk.User{Name: "anna", UID: 1001, Admin: true, Lang: "de"}))
	u := DecodeJSON[sdk.User](t, resp)
	if u != (sdk.User{Name: "anna", UID: 1001, Admin: true, Lang: "de"}) {
		t.Fatalf("unexpected %+v", u)
	}
}
