package sdk

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/gofiber/fiber/v3"
)

// Run starts the extension. With --edelweiss-manifest it prints the manifest
// and exits; otherwise it serves on the listener provided by edelweissd (FD in
// EDELWEISS_LISTEN_FD) or, for development, on EDELWEISS_DEV_LISTEN. It shuts
// down gracefully on SIGTERM/SIGINT or when stdin reaches EOF (edelweissd gone).
func (e *Extension) Run() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if err := e.run(ctx, os.Args, os.Stdout, os.Stdin); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", e.Manifest.ID, err)
		os.Exit(1)
	}
}

func (e *Extension) run(ctx context.Context, args []string, stdout io.Writer, stdin io.Reader) error {
	if len(args) > 1 && args[1] == ManifestFlag {
		data, err := e.ManifestJSON()
		if err != nil {
			return err
		}
		_, err = stdout.Write(append(data, '\n'))
		return err
	}

	ln, inherited, err := listenerFromEnv()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if inherited && stdin != nil {
		go func() {
			_, _ = io.Copy(io.Discard, stdin)
			cancel()
		}()
	}

	served := make(chan error, 1)
	go func() {
		served <- e.App.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true})
	}()
	select {
	case err := <-served:
		return err
	case <-ctx.Done():
	}
	err = e.App.ShutdownWithTimeout(5 * time.Second)
	// fasthttp's Shutdown is a no-op while Serve has not registered the
	// listener yet (a signal or stdin EOF during startup). Closing the
	// listener ourselves guarantees Serve returns in that case; after a normal
	// shutdown the listener is already closed and this is harmless.
	_ = ln.Close()
	if err != nil {
		return err
	}
	return <-served
}

// listenerFromEnv returns the listener inherited from edelweissd or a TCP
// listener for development.
func listenerFromEnv() (net.Listener, bool, error) {
	if v := os.Getenv(EnvListenFD); v != "" {
		fd, err := strconv.Atoi(v)
		if err != nil || fd < 3 {
			return nil, false, fmt.Errorf("invalid %s=%q", EnvListenFD, v)
		}
		f := os.NewFile(uintptr(fd), "edelweiss-listener")
		ln, err := net.FileListener(f)
		_ = f.Close()
		if err != nil {
			return nil, false, fmt.Errorf("inherit listener: %w", err)
		}
		return ln, true, nil
	}
	if addr := os.Getenv(EnvDevListen); addr != "" {
		ln, err := net.Listen("tcp", addr)
		return ln, false, err
	}
	return nil, false, errors.New("not started by edelweissd: set " + EnvDevListen + " (e.g. 127.0.0.1:9101) for development")
}
