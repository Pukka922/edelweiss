// Command testext is a minimal edelweiss extension used by core tests.
package main

import (
	"embed"
	"encoding/json"
	"io/fs"
	"os"
	"strings"
	"sync"

	"github.com/gofiber/contrib/v3/websocket"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/sse"

	"github.com/Pukka922/edelweiss/sdk"
)

//go:embed manifest.json
var manifest []byte

//go:embed ui
var uiFiles embed.FS

// manifestVersion replaces the manifest's version when set at build time (-ldflags "-X main.manifestVersion=1.1.0"),
// so tests can build newer "releases" of this extension.
var manifestVersion string

// manifestRequires sets the manifest's requires.apk when set at build time (comma-separated,
// -ldflags "-X main.manifestRequires=musl"), so the dev environment can release versions with package requirements.
var manifestRequires string

func withOverrides(data []byte, version, requires string) []byte {
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		panic(err)
	}
	if version != "" {
		m["version"] = version
	}
	if requires != "" {
		m["requires"] = map[string]any{"apk": strings.Split(requires, ",")}
	}
	out, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}
	return out
}

func main() {
	if manifestVersion != "" || manifestRequires != "" {
		manifest = withOverrides(manifest, manifestVersion, manifestRequires)
	}
	ui, err := fs.Sub(uiFiles, "ui")
	if err != nil {
		panic(err)
	}
	ext := sdk.New(sdk.Options{Manifest: manifest, UI: ui})
	app := ext.App

	app.Get("/hello", func(c fiber.Ctx) error {
		u := sdk.UserFrom(c)
		groups, _ := os.Getgroups()
		return c.JSON(fiber.Map{"user": u.Name, "uid": u.UID, "admin": u.Admin, "lang": u.Lang,
			"osUid": os.Getuid(), "groups": groups})
	})

	// The manifest version of this binary: the dev environment's smoke test and the page tell 0.1.0 from 0.2.0 by it.
	app.Get("/version", func(c fiber.Ctx) error { return c.JSON(fiber.Map{"version": ext.Manifest.Version}) })

	app.Get("/headers", func(c fiber.Ctx) error { return c.JSON(c.GetReqHeaders()) })

	app.Post("/echo", func(c fiber.Ctx) error { return c.Send(c.Body()) })

	var releaseOnce sync.Once
	release := make(chan struct{})
	app.Post("/release", func(c fiber.Ctx) error {
		releaseOnce.Do(func() { close(release) })
		return c.SendStatus(fiber.StatusNoContent)
	})
	app.Get("/events", sdk.SSE(func(c fiber.Ctx, s *sse.Stream) error {
		if err := s.Event(sse.Event{Name: "tick", Data: "1"}); err != nil {
			return err
		}
		select {
		case <-release:
		case <-s.Done():
			return nil
		}
		return s.Event(sse.Event{Name: "tick", Data: "2"})
	}))

	var busyMu sync.Mutex
	var busyDone func()
	app.Get("/busy", func(c fiber.Ctx) error {
		busyMu.Lock()
		defer busyMu.Unlock()
		if c.Query("on") == "1" && busyDone == nil {
			busyDone = sdk.Busy()
		} else if c.Query("on") == "0" && busyDone != nil {
			busyDone()
			busyDone = nil
		}
		return c.SendStatus(fiber.StatusNoContent)
	})

	app.Get("/ws", func(c fiber.Ctx) error {
		if !websocket.IsWebSocketUpgrade(c) {
			return fiber.ErrUpgradeRequired
		}
		return c.Next()
	}, websocket.New(func(conn *websocket.Conn) {
		for {
			mt, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if err := conn.WriteMessage(mt, msg); err != nil {
				return
			}
		}
	}))

	ext.Run()
}
