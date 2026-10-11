package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
)

func TestRunManifestFlag(t *testing.T) {
	ext := New(Options{Manifest: []byte(testManifest), UI: testUI()})
	var out bytes.Buffer
	if err := ext.run(context.Background(), []string{"ext", ManifestFlag}, &out, nil); err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(out.Bytes(), &m); err != nil {
		t.Fatalf("output is not JSON: %q", out.String())
	}
	if m["id"] != "demo" {
		t.Fatalf("id = %v", m["id"])
	}
}

func TestRunWithoutListenerFails(t *testing.T) {
	t.Setenv(EnvListenFD, "")
	t.Setenv(EnvDevListen, "")
	ext := New(Options{Manifest: []byte(testManifest), UI: testUI()})
	if err := ext.run(context.Background(), []string{"ext"}, io.Discard, nil); err == nil {
		t.Fatal("expected error without listener")
	}
}

// unixClient returns an HTTP client that dials the given socket.
func unixClient(sock string) *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		},
	}, Timeout: 5 * time.Second}
}

func TestRunServesOnInheritedFDAndStopsOnStdinEOF(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "ext.sock")
	ul, err := net.ListenUnix("unix", &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := ul.File() // dup'ed fd, like the one edelweissd passes as FD 3
	if err != nil {
		t.Fatal(err)
	}
	ul.SetUnlinkOnClose(false)
	_ = ul.Close()
	// hand over a raw fd that run() owns and closes (avoids a double close via f)
	fd, err := syscall.Dup(int(f.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	t.Setenv(EnvListenFD, strconv.Itoa(fd))

	ext := New(Options{Manifest: []byte(testManifest), UI: testUI()})
	ext.App.Get("/hello", func(c fiber.Ctx) error { return c.SendString("hi " + UserFrom(c).Name) })

	stdinR, stdinW := io.Pipe()
	errc := make(chan error, 1)
	go func() { errc <- ext.run(context.Background(), []string{"ext"}, io.Discard, stdinR) }()

	req, _ := http.NewRequest("GET", "http://ext/hello", nil)
	req.Header.Set(HeaderUser, "myUser")
	resp, err := unixClient(sock).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "hi myUser" {
		t.Fatalf("body %q", body)
	}

	_ = stdinW.Close() // parent died → EOF → graceful shutdown
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("run returned %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not stop after stdin EOF")
	}
}

func TestRunDevListenStopsOnContextCancel(t *testing.T) {
	t.Setenv(EnvListenFD, "")
	t.Setenv(EnvDevListen, "127.0.0.1:0")
	ext := New(Options{Manifest: []byte(testManifest), UI: testUI()})
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- ext.run(ctx, []string{"ext"}, io.Discard, nil) }()
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("run returned %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not stop after cancel")
	}
}

// A shutdown request that arrives before the server has started accepting
// (SIGTERM during startup, stdin already at EOF) must still stop run().
func TestRunStopsWhenContextAlreadyCancelled(t *testing.T) {
	t.Setenv(EnvListenFD, "")
	t.Setenv(EnvDevListen, "127.0.0.1:0")
	ext := New(Options{Manifest: []byte(testManifest), UI: testUI()})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	errc := make(chan error, 1)
	go func() { errc <- ext.run(ctx, []string{"ext"}, io.Discard, nil) }()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("run returned %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("run did not stop with an already cancelled context")
	}
}
