package server

import (
	"context"
	"errors"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/Pukka922/edelweiss/core/internal/github"
	"github.com/Pukka922/edelweiss/core/internal/selfupdate"
	"github.com/Pukka922/edelweiss/sdk"
)

// SelfUpdater is implemented by *selfupdate.Service.
type SelfUpdater interface {
	About() selfupdate.About
	// Check is cached for two minutes; refresh asks GitHub anyway.
	Check(ctx context.Context, refresh bool) (selfupdate.CheckView, error)
	// Start launches the update to the latest release; confirmed is the SHA-256 the user saw in the check.
	Start(ctx context.Context, confirmed string) (selfupdate.Offer, error)
	Dismiss() error
}

const (
	aboutCheckTimeout = 60 * time.Second
	selfUpdateTimeout = installTimeout // a download of up to max_download_size
)

type selfUpdateRequest struct {
	SHA256 string `json:"sha256"`
}

func (s *Server) selfUpdater() (SelfUpdater, error) {
	if s.d.SelfUpdate == nil {
		return nil, sdk.NewError(fiber.StatusServiceUnavailable, "self_update_unavailable", "the self-update is not available")
	}
	return s.d.SelfUpdate, nil
}

// about is GET /api/about, for every signed-in user. That a lbu commit failed (lbuFailed) is for everybody: after the
// update everyone is signed out, and the page must tell the admin who signs in again that the update does not survive a
// reboot of the host. The raw output of the failed commit is admin information: it is only in the answer in admin mode.
func (s *Server) about(c fiber.Ctx) error {
	su, err := s.selfUpdater()
	if err != nil {
		return err
	}
	a := su.About()
	if r := a.State.Result; r != nil && r.LbuError != "" {
		shown := *r // a copy: the updater's result stays as it is
		shown.LbuFailed = true
		if !sessionFrom(c).Admin {
			shown.LbuError = ""
		}
		a.State.Result = &shown
	}
	return c.JSON(a)
}

// aboutCheck is GET /api/about/update[?refresh=1].
func (s *Server) aboutCheck(c fiber.Ctx) error {
	su, err := s.selfUpdater()
	if err != nil {
		return err
	}
	ctx, cancel := requestContext(c, aboutCheckTimeout)
	defer cancel()
	v, err := su.Check(ctx, c.Query("refresh") == "1")
	if err != nil {
		return s.selfUpdateError("update check", err)
	}
	return c.JSON(v)
}

// aboutStart is POST /api/about/update {sha256}: 202 once the helper runs, which restarts edelweissd.
func (s *Server) aboutStart(c fiber.Ctx) error {
	su, err := s.selfUpdater()
	if err != nil {
		return err
	}
	var req selfUpdateRequest
	if err := decodeJSON(c, &req); err != nil {
		return err
	}
	if req.SHA256 == "" {
		return sdk.NewError(fiber.StatusBadRequest, "invalid_input", "sha256 is required: the checksum of the offered update")
	}
	ctx, cancel := requestContext(c, selfUpdateTimeout)
	defer cancel()
	off, err := su.Start(ctx, req.SHA256)
	if err != nil {
		return s.selfUpdateError("update", err)
	}
	s.d.Logger.Info("self-update started", "user", sessionFrom(c).User.Name, "from", off.Current, "to", off.Latest,
		"asset", off.Asset, "sha256", off.SHA256)
	return c.Status(fiber.StatusAccepted).JSON(fiber.Map{"to": off.Latest})
}

// aboutDismiss is DELETE /api/about/update/result.
func (s *Server) aboutDismiss(c fiber.Ctx) error {
	su, err := s.selfUpdater()
	if err != nil {
		return err
	}
	if err := su.Dismiss(); err != nil {
		return s.selfUpdateError("dismiss", err)
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// selfUpdateErrors maps the errors of the self-update to HTTP answers; the first match wins. The context errors come
// first, as in installerErrors; the GitHub errors are those of installerStatus.
var selfUpdateErrors = []struct {
	err    error
	status int
	code   string
}{
	{context.DeadlineExceeded, fiber.StatusGatewayTimeout, "timeout"},
	{context.Canceled, fiber.StatusGatewayTimeout, "timeout"},
	{selfupdate.ErrNotPossible, fiber.StatusConflict, "not_possible"},
	{selfupdate.ErrNoUpdate, fiber.StatusConflict, "no_update"},
	{selfupdate.ErrBusy, fiber.StatusConflict, "operation_in_progress"},
	{selfupdate.ErrHelperRunning, fiber.StatusConflict, "operation_in_progress"},
	{selfupdate.ErrReleaseChanged, fiber.StatusConflict, "release_changed"},
	{selfupdate.ErrChecksumMismatch, fiber.StatusUnprocessableEntity, "checksum_mismatch"},
	{selfupdate.ErrWrongVersion, fiber.StatusUnprocessableEntity, "wrong_version"},
	{selfupdate.ErrNotRunnable, fiber.StatusUnprocessableEntity, "not_runnable"},
	{selfupdate.ErrBadScript, fiber.StatusUnprocessableEntity, "bad_script"},
	{selfupdate.ErrNoSpace, fiber.StatusConflict, "no_space"},
	{selfupdate.ErrNoAsset, fiber.StatusNotFound, "no_asset"},
	{selfupdate.ErrNoChecksums, fiber.StatusUnprocessableEntity, "no_checksums"},
	{selfupdate.ErrChecksumsDisagree, fiber.StatusUnprocessableEntity, "checksums_disagree"},
	{github.ErrTooLarge, fiber.StatusRequestEntityTooLarge, "asset_too_large"},
}

// selfUpdateStatus is the HTTP status and error code for err (500 internal_error if nothing matches).
func selfUpdateStatus(err error) (int, string) {
	for _, m := range selfUpdateErrors {
		if errors.Is(err, m.err) {
			return m.status, m.code
		}
	}
	return installerStatus(err)
}

func (s *Server) selfUpdateError(op string, err error) error {
	status, code := selfUpdateStatus(err)
	if status == fiber.StatusInternalServerError {
		s.d.Logger.Error("self-update: the "+op+" failed", "err", err)
		return sdk.NewError(status, code, "the "+op+" failed")
	}
	apiErr := sdk.NewError(status, code, err.Error())
	var np *selfupdate.NotPossibleError
	if errors.As(err, &np) {
		apiErr = apiErr.WithDetails(map[string]any{"reason": np.Reason})
	}
	return apiErr
}
