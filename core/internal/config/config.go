// Package config loads and validates /etc/edelweiss/edelweiss.toml.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// DefaultPath is the default configuration file location.
const DefaultPath = "/etc/edelweiss/edelweiss.toml"

// Duration is a time.Duration that decodes from strings like "15m".
type Duration struct{ time.Duration }

func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", b, err)
	}
	d.Duration = v
	return nil
}

func (d Duration) MarshalText() ([]byte, error) { return []byte(d.Duration.String()), nil }

// ByteSize is a size in bytes that decodes from strings like "200MB".
type ByteSize int64

func (s *ByteSize) UnmarshalText(b []byte) error {
	v, err := ParseByteSize(string(b))
	if err != nil {
		return err
	}
	*s = ByteSize(v)
	return nil
}

// ParseByteSize parses "100", "1KB"/"1KiB", "200MB"/"200MiB", "1GB"/"1GiB" (1024-based).
func ParseByteSize(s string) (int64, error) {
	str := strings.ToUpper(strings.TrimSpace(s))
	mult := int64(1)
	for _, u := range []struct {
		suffix string
		mult   int64
	}{{"GIB", 1 << 30}, {"MIB", 1 << 20}, {"KIB", 1 << 10}, {"GB", 1 << 30}, {"MB", 1 << 20}, {"KB", 1 << 10}, {"B", 1}} {
		if strings.HasSuffix(str, u.suffix) {
			mult = u.mult
			str = strings.TrimSpace(strings.TrimSuffix(str, u.suffix))
			break
		}
	}
	n, err := strconv.ParseInt(str, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	if n > math.MaxInt64/mult {
		return 0, fmt.Errorf("size %q overflows int64", s)
	}
	return n * mult, nil
}

type Config struct {
	Listen     string     `toml:"listen"`
	TLS        TLS        `toml:"tls"`
	Auth       Auth       `toml:"auth"`
	Extensions Extensions `toml:"extensions"`
	GitHub     GitHub     `toml:"github"`
	Log        Log        `toml:"log"`
	Paths      Paths      `toml:"paths"`
	Dev        Dev        `toml:"dev"`
}

type TLS struct {
	Enabled bool   `toml:"enabled"`
	Cert    string `toml:"cert"`
	Key     string `toml:"key"`
}

type Auth struct {
	AdminGroup         string   `toml:"admin_group"`
	DenyUsers          []string `toml:"deny_users"`
	SessionIdleTimeout Duration `toml:"session_idle_timeout"`
	SessionMaxAge      Duration `toml:"session_max_age"`
	ShadowFile         string   `toml:"shadow_file"`
}

type Extensions struct {
	BackendIdleTimeout Duration `toml:"backend_idle_timeout"`
	MaxDownloadSize    ByteSize `toml:"max_download_size"`
}

type GitHub struct {
	APIURL string `toml:"api_url"`
	Token  string `toml:"token"`
}

type Log struct {
	Level string `toml:"level"`
}

type Paths struct {
	StateDir string `toml:"state_dir"`
	RunDir   string `toml:"run_dir"`
}

type Dev struct {
	ShellDir   string         `toml:"shell_dir"`
	Extensions []DevExtension `toml:"extensions"`
}

// DevExtension is an extension loaded directly from a build output (--dev).
type DevExtension struct {
	Binary string `toml:"binary"`
	UIDir  string `toml:"ui_dir"`
}

// Default returns the built-in configuration.
func Default() *Config {
	return &Config{
		Listen: "0.0.0.0:9090",
		TLS:    TLS{Enabled: true},
		Auth: Auth{
			AdminGroup:         "wheel",
			DenyUsers:          []string{"root"},
			SessionIdleTimeout: Duration{15 * time.Minute},
			SessionMaxAge:      Duration{12 * time.Hour},
			ShadowFile:         "/etc/shadow",
		},
		Extensions: Extensions{BackendIdleTimeout: Duration{10 * time.Minute}, MaxDownloadSize: 200 << 20},
		GitHub:     GitHub{APIURL: "https://api.github.com"},
		Log:        Log{Level: "info"},
		Paths:      Paths{StateDir: "/var/lib/edelweiss", RunDir: "/run/edelweiss"},
	}
}

// Load reads path on top of the defaults and validates the result.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c := Default()
	dec := toml.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(c); err != nil {
		// go-toml's Error() text omits the offending key and position; String() includes them.
		var strict *toml.StrictMissingError
		var derr *toml.DecodeError
		switch {
		case errors.As(err, &strict):
			return nil, fmt.Errorf("config %s: %s", path, strict.String())
		case errors.As(err, &derr):
			return nil, fmt.Errorf("config %s: %s", path, derr.String())
		}
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	return c, nil
}

// Validate checks all settings.
func (c *Config) Validate() error {
	var errs []error
	if _, port, err := net.SplitHostPort(c.Listen); err != nil {
		errs = append(errs, fmt.Errorf("listen: %w", err))
	} else if p, perr := strconv.Atoi(port); perr != nil || p < 1 || p > 65535 {
		errs = append(errs, fmt.Errorf("listen: invalid port %q (want 1-65535)", port))
	}
	if (c.TLS.Cert == "") != (c.TLS.Key == "") {
		errs = append(errs, errors.New("tls: cert and key must be set together"))
	}
	if c.Auth.AdminGroup == "" {
		errs = append(errs, errors.New("auth.admin_group must not be empty"))
	}
	if c.Auth.SessionIdleTimeout.Duration <= 0 || c.Auth.SessionMaxAge.Duration <= 0 {
		errs = append(errs, errors.New("auth: session timeouts must be positive"))
	}
	if c.Extensions.BackendIdleTimeout.Duration <= 0 {
		errs = append(errs, errors.New("extensions.backend_idle_timeout must be positive"))
	}
	if c.Extensions.MaxDownloadSize <= 0 {
		errs = append(errs, errors.New("extensions.max_download_size must be positive"))
	}
	if _, err := parseLevel(c.Log.Level); err != nil {
		errs = append(errs, fmt.Errorf("log.level: %w", err))
	}
	for name, p := range map[string]string{"paths.state_dir": c.Paths.StateDir, "paths.run_dir": c.Paths.RunDir, "auth.shadow_file": c.Auth.ShadowFile} {
		if !filepath.IsAbs(p) {
			errs = append(errs, fmt.Errorf("%s must be an absolute path", name))
		}
	}
	if c.Dev.ShellDir != "" && !filepath.IsAbs(c.Dev.ShellDir) {
		errs = append(errs, errors.New("dev.shell_dir must be an absolute path"))
	}
	for i, d := range c.Dev.Extensions {
		if !filepath.IsAbs(d.Binary) || (d.UIDir != "" && !filepath.IsAbs(d.UIDir)) {
			errs = append(errs, fmt.Errorf("dev.extensions[%d]: binary and ui_dir must be absolute paths", i))
		}
	}
	return errors.Join(errs...)
}

// LogLevel returns the configured slog level.
func (c *Config) LogLevel() slog.Level {
	l, _ := parseLevel(c.Log.Level)
	return l
}

func parseLevel(s string) (slog.Level, error) {
	var l slog.Level
	err := l.UnmarshalText([]byte(s))
	return l, err
}

func (c *Config) ExtensionsDir() string { return filepath.Join(c.Paths.StateDir, "extensions") }
func (c *Config) TmpDir() string        { return filepath.Join(c.Paths.StateDir, "tmp") }
func (c *Config) TLSDir() string        { return filepath.Join(c.Paths.StateDir, "tls") }
func (c *Config) SocketDir() string     { return filepath.Join(c.Paths.RunDir, "sockets") }

// EnsureDirs creates the state and run directories with the expected modes.
// The state directory must be traversable (0711) so extension processes and
// the manifest query (user nobody) can reach their binaries.
func (c *Config) EnsureDirs() error {
	for _, d := range []struct {
		path string
		mode os.FileMode
	}{{c.Paths.StateDir, 0o711}, {c.Paths.RunDir, 0o755}} {
		if err := os.MkdirAll(d.path, d.mode); err != nil {
			return err
		}
		if err := os.Chmod(d.path, d.mode); err != nil {
			return err
		}
	}
	return nil
}
