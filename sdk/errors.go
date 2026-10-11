package sdk

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gofiber/fiber/v3"
)

// APIError is an error with an HTTP status and a stable machine-readable code.
type APIError struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
	// Details are further fields of the error object, e.g. {"packages": ["docker"]}; "code" and "message" in it are
	// ignored.
	Details map[string]any `json:"-"`
}

func (e *APIError) Error() string { return fmt.Sprintf("%d %s: %s", e.Status, e.Code, e.Message) }

// NewError creates an APIError. code should be snake_case, e.g. "invalid_hostname".
func NewError(status int, code, message string) *APIError {
	return &APIError{Status: status, Code: code, Message: message}
}

// WithDetails sets further fields of the error object and returns e.
func (e *APIError) WithDetails(details map[string]any) *APIError {
	e.Details = details
	return e
}

// WriteError writes the edelweiss error envelope.
func WriteError(c fiber.Ctx, status int, code, message string) error {
	return writeError(c, status, code, message, nil)
}

func writeError(c fiber.Ctx, status int, code, message string, details map[string]any) error {
	obj := fiber.Map{}
	for k, v := range details {
		obj[k] = v
	}
	obj["code"] = code
	obj["message"] = message
	return c.Status(status).JSON(fiber.Map{"error": obj})
}

// quietCodes are server-error codes not logged per request: edelweissd answers
// them for every request to an extension that cannot be started, reached or is
// crash-looping, and logs the cause where it happens instead.
var quietCodes = map[string]bool{"backend_unavailable": true, "backend_crashloop": true}

// ErrorHandler is a fiber.ErrorHandler producing the edelweiss error envelope.
// Server errors (5xx) are logged with slog.Error, except those with a code in
// quietCodes; client errors are not logged.
func ErrorHandler(c fiber.Ctx, err error) error {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		if !quietCodes[apiErr.Code] {
			logServerError(c, apiErr.Status, err)
		}
		return writeError(c, apiErr.Status, apiErr.Code, apiErr.Message, apiErr.Details)
	}
	var fe *fiber.Error
	if errors.As(err, &fe) {
		logServerError(c, fe.Code, err)
		return WriteError(c, fe.Code, codeForStatus(fe.Code), fe.Message)
	}
	slog.Error("unhandled error", "method", c.Method(), "path", c.Path(), "err", err)
	return WriteError(c, fiber.StatusInternalServerError, "internal_error", "internal server error")
}

func logServerError(c fiber.Ctx, status int, err error) {
	if status >= fiber.StatusInternalServerError {
		slog.Error("server error", "method", c.Method(), "path", c.Path(), "status", status, "err", err)
	}
}

func codeForStatus(status int) string {
	switch status {
	case fiber.StatusBadRequest:
		return "bad_request"
	case fiber.StatusUnauthorized:
		// Not "unauthenticated": that code is reserved for the core ("the session is gone", it sends the user to the
		// login page). A 401 of an extension must not sign anybody out.
		return "unauthorized"
	case fiber.StatusForbidden:
		return "forbidden"
	case fiber.StatusNotFound:
		return "not_found"
	case fiber.StatusMethodNotAllowed:
		return "method_not_allowed"
	case fiber.StatusRequestEntityTooLarge:
		return "payload_too_large"
	case fiber.StatusTooManyRequests:
		return "too_many_requests"
	default:
		// fiber v3 does not export StatusMessage; net/http.StatusText is the equivalent.
		text := http.StatusText(status)
		if text == "" {
			return "error"
		}
		return strings.ToLower(strings.ReplaceAll(text, " ", "_"))
	}
}
