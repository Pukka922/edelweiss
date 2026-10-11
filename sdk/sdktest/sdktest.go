// Package sdktest helps testing edelweiss extension handlers without edelweissd.
package sdktest

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/Pukka922/edelweiss/sdk"
)

// Request builds a request carrying the identity headers edelweissd would set.
func Request(method, target string, body io.Reader, u sdk.User) *http.Request {
	req := httptest.NewRequest(method, target, body)
	req.Header.Set(sdk.HeaderUser, u.Name)
	req.Header.Set(sdk.HeaderUID, strconv.Itoa(u.UID))
	if u.Admin {
		req.Header.Set(sdk.HeaderAdmin, "1")
	} else {
		req.Header.Set(sdk.HeaderAdmin, "0")
	}
	if u.Lang != "" {
		req.Header.Set(sdk.HeaderLang, u.Lang)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

// Do runs the request against app with a 5 s timeout.
func Do(t testing.TB, app *fiber.App, req *http.Request) *http.Response {
	t.Helper()
	resp, err := app.Test(req, fiber.TestConfig{Timeout: 5 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("request %s %s: %v", req.Method, req.URL, err)
	}
	return resp
}

// DecodeJSON decodes the response body into T.
func DecodeJSON[T any](t testing.TB, resp *http.Response) T {
	t.Helper()
	defer resp.Body.Close()
	var v T
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return v
}
