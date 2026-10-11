package server

import (
	"context"
	"errors"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/Pukka922/edelweiss/core/internal/extmgr"
	"github.com/Pukka922/edelweiss/core/internal/github"
	"github.com/Pukka922/edelweiss/sdk"
)

const (
	resolveTimeout = 30 * time.Second
	installTimeout = 15 * time.Minute // a download of up to max_download_size
)

// requestContext ends when d has passed or the server shuts down. It carries none of the fasthttp request
// context's values: that context is recycled when the handler returns, while dial goroutines of the GitHub
// client may still read values from the context they were given.
func requestContext(c fiber.Ctx, d time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	shutdown := c.RequestCtx().Done() // read inside the handler; only the channel is kept (nil under app.Test)
	go func() {
		select {
		case <-shutdown:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

type sourceRequest struct {
	Source string `json:"source"`
}

type installRequest struct {
	Source string `json:"source"`
	ID     string `json:"id"`
	SHA256 string `json:"sha256"`
}

// updateRequest is the optional body of an update: the SHA-256 the user saw in the update list.
type updateRequest struct {
	SHA256 string `json:"sha256"`
}

type updateErrorView struct {
	ID      string `json:"id"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (s *Server) installer() (Installer, error) {
	if s.d.Installer == nil {
		return nil, sdk.NewError(fiber.StatusServiceUnavailable, "installer_unavailable", "installing from GitHub is not available")
	}
	return s.d.Installer, nil
}

func (s *Server) resolveExtension(c fiber.Ctx) error {
	inst, err := s.installer()
	if err != nil {
		return err
	}
	var req sourceRequest
	if err := decodeJSON(c, &req); err != nil {
		return err
	}
	ctx, cancel := requestContext(c, resolveTimeout)
	defer cancel()
	res, err := inst.Resolve(ctx, req.Source)
	if err != nil {
		return s.installerError("resolve", err)
	}
	return c.JSON(res)
}

func (s *Server) installExtension(c fiber.Ctx) error {
	inst, err := s.installer()
	if err != nil {
		return err
	}
	var req installRequest
	if err := decodeJSON(c, &req); err != nil {
		return err
	}
	ctx, cancel := requestContext(c, installTimeout)
	defer cancel()
	ext, err := inst.Install(ctx, extmgr.InstallRequest{Source: req.Source, ID: req.ID, SHA256: req.SHA256})
	if err != nil {
		return s.installerError("install", err)
	}
	s.audit(c, "extension installed", ext)
	return c.Status(fiber.StatusCreated).JSON(s.extView(ext))
}

func (s *Server) extensionUpdates(c fiber.Ctx) error {
	inst, err := s.installer()
	if err != nil {
		return err
	}
	ctx, cancel := requestContext(c, 2*resolveTimeout)
	defer cancel()
	updates, failed := inst.Updates(ctx)
	errs := make([]updateErrorView, 0, len(failed))
	for _, f := range failed {
		status, code := installerStatus(f.Err)
		msg := f.Err.Error()
		if status == fiber.StatusInternalServerError {
			s.d.Logger.Error("extension update check failed", "id", f.ID, "err", f.Err)
			msg = "the update check failed"
		}
		errs = append(errs, updateErrorView{ID: f.ID, Code: code, Message: msg})
	}
	return c.JSON(fiber.Map{"updates": updates, "errors": errs})
}

func (s *Server) updateExtension(c fiber.Ctx) error {
	inst, err := s.installer()
	if err != nil {
		return err
	}
	var req updateRequest
	if len(c.BodyRaw()) > 0 { // the body is optional; decodeJSON refuses an empty one
		if err := decodeJSON(c, &req); err != nil {
			return err
		}
	}
	ctx, cancel := requestContext(c, installTimeout)
	defer cancel()
	ext, err := inst.Update(ctx, c.Params("id"), req.SHA256)
	if err != nil {
		return s.installerError("update", err)
	}
	s.audit(c, "extension updated", ext)
	return c.JSON(s.extView(ext))
}

func (s *Server) audit(c fiber.Ctx, msg string, ext *extmgr.Extension) {
	s.d.Logger.Info(msg, "id", ext.Manifest.ID, "version", ext.Manifest.Version, "repo", ext.Source.Repo,
		"tag", ext.Source.Tag, "asset", ext.Source.Asset, "sha256", ext.Source.SHA256, "user", sessionFrom(c).User.Name)
}

// installerErrors maps installer and GitHub errors to HTTP answers; the first match wins. The context errors come
// first: a timeout or a shutdown while talking to GitHub is wrapped in ErrUnavailable, and must not look like an outage.
var installerErrors = []struct {
	err    error
	status int
	code   string
}{
	{context.DeadlineExceeded, fiber.StatusGatewayTimeout, "timeout"},
	{context.Canceled, fiber.StatusGatewayTimeout, "timeout"}, // the request context ends when the server shuts down
	{extmgr.ErrBusy, fiber.StatusConflict, "operation_in_progress"},
	{extmgr.ErrNotFound, fiber.StatusNotFound, "extension_not_found"},
	{extmgr.ErrNotFromGitHub, fiber.StatusConflict, "not_from_github"},
	{extmgr.ErrUpToDate, fiber.StatusConflict, "up_to_date"},
	{extmgr.ErrReleaseChanged, fiber.StatusConflict, "release_changed"},
	{extmgr.ErrChooseID, fiber.StatusBadRequest, "id_required"},
	{extmgr.ErrUnknownCandidate, fiber.StatusBadRequest, "unknown_id"},
	{extmgr.ErrNoCandidates, fiber.StatusUnprocessableEntity, "no_matching_assets"},
	{extmgr.ErrNoChecksums, fiber.StatusUnprocessableEntity, "checksums_missing"},
	{extmgr.ErrChecksumMismatch, fiber.StatusUnprocessableEntity, "checksum_mismatch"},
	{extmgr.ErrMissingPackages, fiber.StatusUnprocessableEntity, "missing_packages"},
	{extmgr.ErrBadArchive, fiber.StatusUnprocessableEntity, "invalid_extension"},
	{extmgr.ErrInvalidExtension, fiber.StatusUnprocessableEntity, "invalid_extension"},
	{github.ErrInvalidSource, fiber.StatusBadRequest, "invalid_source"},
	{github.ErrReleaseNotFound, fiber.StatusNotFound, "release_not_found"},
	{github.ErrTooLarge, fiber.StatusRequestEntityTooLarge, "asset_too_large"},
	{github.ErrRateLimited, fiber.StatusServiceUnavailable, "github_rate_limited"},
	{github.ErrUnavailable, fiber.StatusBadGateway, "github_unavailable"},
}

// installerStatus is the HTTP status and error code for err (500 internal_error if nothing matches).
func installerStatus(err error) (int, string) {
	for _, m := range installerErrors {
		if errors.Is(err, m.err) {
			return m.status, m.code
		}
	}
	return fiber.StatusInternalServerError, "internal_error"
}

func (s *Server) installerError(op string, err error) error {
	status, code := installerStatus(err)
	if status == fiber.StatusInternalServerError {
		s.d.Logger.Error("extension "+op+" failed", "err", err)
		return sdk.NewError(status, code, "the "+op+" failed")
	}
	apiErr := sdk.NewError(status, code, err.Error())
	var mp *extmgr.MissingPackagesError
	if errors.As(err, &mp) {
		apiErr = apiErr.WithDetails(map[string]any{"packages": mp.Packages})
	}
	return apiErr
}
