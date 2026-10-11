// Package daemon wires all components of edelweissd together.
package daemon

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"golang.org/x/sys/unix"

	"github.com/Pukka922/edelweiss/core/internal/auth"
	"github.com/Pukka922/edelweiss/core/internal/config"
	"github.com/Pukka922/edelweiss/core/internal/extmgr"
	"github.com/Pukka922/edelweiss/core/internal/github"
	"github.com/Pukka922/edelweiss/core/internal/procman"
	"github.com/Pukka922/edelweiss/core/internal/server"
	"github.com/Pukka922/edelweiss/core/internal/session"
	"github.com/Pukka922/edelweiss/core/internal/tlsutil"
	"github.com/Pukka922/edelweiss/core/internal/version"
	"github.com/Pukka922/edelweiss/core/internal/webui"
)

// certCheckInterval is how often a running daemon re-checks its self-signed certificate.
const certCheckInterval = 24 * time.Hour

// shutdownTimeout bounds how long in-flight HTTP requests may finish on shutdown.
const shutdownTimeout = 10 * time.Second

// Run starts edelweissd and blocks until ctx is cancelled or the listener fails.
func Run(ctx context.Context, cfg *config.Config, logger *slog.Logger) error {
	ctx, cancel := context.WithCancel(ctx) // stops the background goroutines on every return path
	defer cancel()
	if err := cfg.EnsureDirs(); err != nil {
		return err
	}
	lock, err := lockRunDir(cfg.Paths.RunDir)
	if err != nil {
		return err
	}
	defer lock.Close()
	verifier, err := auth.NewVerifier(cfg.Auth.ShadowFile)
	if err != nil {
		return err
	}
	authn := &auth.Authenticator{Verifier: verifier, Users: auth.OSUserDB{}, DenyUsers: cfg.Auth.DenyUsers, AdminGroup: cfg.Auth.AdminGroup}
	sessions := session.NewStore(cfg.Auth.SessionIdleTimeout.Duration, cfg.Auth.SessionMaxAge.Duration)

	pool, err := procman.New(procman.Options{SocketDir: cfg.SocketDir(), IdleTimeout: cfg.Extensions.BackendIdleTimeout.Duration, Logger: logger})
	if err != nil {
		return err
	}
	querier, err := extmgr.NewExecQuerier()
	if err != nil {
		return err
	}
	reg, err := extmgr.New(extmgr.Options{
		Dir: cfg.ExtensionsDir(), TmpDir: cfg.TmpDir(), Querier: querier, Dev: cfg.Dev.Extensions,
		ExtDataRoot: extmgr.DefaultExtDataRoot, Logger: logger, OnChange: pool.StopExtension,
	})
	if err != nil {
		return err
	}
	if err := reg.Load(ctx); err != nil {
		return err
	}
	if err := reg.Watch(ctx); err != nil {
		return err
	}
	installer := extmgr.NewInstaller(extmgr.InstallerOptions{
		Manager:     reg,
		Releases:    github.NewClient(cfg.GitHub.APIURL, cfg.GitHub.Token, "edelweiss/"+version.Version),
		Arch:        runtime.GOARCH,
		MaxDownload: int64(cfg.Extensions.MaxDownloadSize),
		Logger:      logger,
	})
	shellFS, err := webui.FS(cfg.Dev.ShellDir)
	if err != nil {
		return err
	}
	app, err := server.New(server.Deps{
		Config: cfg, Auth: authn, Sessions: sessions, Registry: reg, Installer: installer, Pool: pool, Shell: shellFS,
		Dev: cfg.Dev.ShellDir != "", Version: version.Version, Logger: logger,
		Hostname: func() string { h, _ := os.Hostname(); return h },
	})
	if err != nil {
		return err
	}

	var tlsConfig *tls.Config
	selfSignedCert := "" // the certificate file if it is the self-signed one
	if cfg.TLS.Enabled {
		cert, key := cfg.TLS.Cert, cfg.TLS.Key
		created := false
		if cert == "" {
			if cert, key, created, err = tlsutil.EnsureSelfSigned(cfg.TLSDir(), tlsutil.LocalHosts(), time.Now()); err != nil {
				return err
			}
			selfSignedCert = cert
		}
		pair, err := tls.LoadX509KeyPair(cert, key)
		if err != nil {
			return fmt.Errorf("tls: load certificate %s and key %s: %w", cert, key, err)
		}
		if created {
			// the fingerprint lets the operator check the certificate a browser is shown
			logger.Info("generated a self-signed TLS certificate", "cert", cert, "sha256", fingerprint(pair.Certificate[0]))
		}
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{pair}}
	}
	ln, err := listen(cfg.Listen, tlsConfig, logger)
	if err != nil {
		return err
	}
	// Closing it ends a Serve that started only after the shutdown (fasthttp
	// cannot shut down a listener it does not serve yet); closing twice is harmless.
	defer ln.Close()

	// The pool runs on its own context: pool.Run stops every extension process
	// as soon as its context ends, and that must not happen before the HTTP
	// server has drained, or in-flight proxied requests and streams would be
	// cut at the moment of SIGTERM.
	poolCtx, cancelPool := context.WithCancel(context.WithoutCancel(ctx))
	poolDone := make(chan struct{})
	go func() { defer close(poolDone); pool.Run(poolCtx) }()
	stopPool := func() {
		cancelPool()
		pool.StopAll()
		<-poolDone
	}
	go sweepSessions(ctx, sessions, pool)
	if selfSignedCert != "" {
		go watchSelfSigned(ctx, cfg.TLSDir(), certCheckInterval, logger)
	}
	errc := make(chan error, 1)
	go func() { errc <- app.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true}) }()
	logger.Info("edelweissd started", "version", version.Version, "listen", cfg.Listen, "tls", cfg.TLS.Enabled, "extensions", len(reg.List()))

	select {
	case err := <-errc:
		stopPool()
		return err
	case <-ctx.Done():
	}
	logger.Info("edelweissd shutting down")
	if err := app.ShutdownWithTimeout(shutdownTimeout); err != nil {
		logger.Warn("http server did not drain cleanly, stopping extensions anyway", "err", err)
	}
	_ = ln.Close()
	stopPool()
	return nil
}

// listen opens the listener the HTTP server runs on: IPv4 like Fiber's
// default, at most server.MaxConnsPerIP connections per client address
// (counted before the TLS handshake), and TLS on top if configured.
func listen(addr string, tlsConfig *tls.Config, logger *slog.Logger) (net.Listener, error) {
	ln, err := net.Listen("tcp4", addr)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", addr, err)
	}
	ln = server.LimitListener(ln, server.MaxConnsPerIP, logger)
	if tlsConfig != nil {
		ln = tls.NewListener(ln, tlsConfig)
	}
	return ln, nil
}

// lockFile is the single-instance lock in the run directory.
const lockFile = "edelweissd.lock"

// lockRunDir takes an exclusive lock on the run directory's lock file; it is
// held until the returned file is closed or the process ends. A second daemon
// on the same directories must give up before it changes anything: its process
// manager would delete the sockets of the running daemon's extensions.
func lockRunDir(runDir string) (*os.File, error) {
	path := filepath.Join(runDir, lockFile)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600) // close-on-exec: extensions never hold it
	if err != nil {
		return nil, fmt.Errorf("open lock %s: %w", path, err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("edelweissd is already running (lock %s)", path)
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	// the pid is for the operator only; the lock is what counts
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	}
	return f, nil
}

// watchSelfSigned keeps the self-signed certificate in dir fresh while the
// daemon runs: tlsutil renews it shortly before it expires. The renewed
// certificate is not hot-swapped; a restart is needed to load it, which is
// logged once per renewal.
func watchSelfSigned(ctx context.Context, dir string, interval time.Duration, logger *slog.Logger) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		cert, _, renewed, err := tlsutil.EnsureSelfSigned(dir, tlsutil.LocalHosts(), time.Now())
		if err != nil {
			logger.Warn("renewing self-signed certificate failed", "err", err)
			continue
		}
		if renewed {
			logger.Info("self-signed certificate was renewed; restart edelweissd to load it", "cert", cert)
		}
	}
}

// fingerprint is the SHA-256 fingerprint of a DER certificate as browsers and
// `openssl x509 -fingerprint -sha256` show it.
func fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return strings.ReplaceAll(fmt.Sprintf("% X", sum[:]), " ", ":")
}

func sweepSessions(ctx context.Context, sessions *session.Store, pool *procman.Manager) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for _, s := range sessions.Sweep() {
				if !sessions.HasSession(s.User.Name, false) {
					pool.StopUser(s.User.Name, false)
				} else if !sessions.HasSession(s.User.Name, true) {
					pool.StopUser(s.User.Name, true)
				}
			}
		}
	}
}
