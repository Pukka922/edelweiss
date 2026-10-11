// Package service installs edelweissd as an OpenRC service, as used by Alpine Linux.
package service

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	InitScript = "etc/init.d/edelweiss"
	ConfD      = "etc/conf.d/edelweiss"
	ConfigDir  = "etc/edelweiss"
	ConfigFile = "etc/edelweiss/edelweiss.toml"
	StateDir   = "var/lib/edelweiss"
)

// Options say where and how the service is installed. Tests point Root at a temporary directory and replace Run.
type Options struct {
	Root   string                                  // file system root; "" means "/"
	Binary string                                  // absolute path of the edelweissd binary the service runs
	Run    func(name string, args ...string) error // runs rc-update and rc-service; nil: execute them
	Out    io.Writer                               // progress messages; nil: discarded
}

// safeBinary keeps the path usable inside the double-quoted command= of the init script.
var safeBinary = regexp.MustCompile(`^/[A-Za-z0-9/._+-]+$`)

// lookOpenRC finds one of OpenRC's commands, with a clear error for a system without OpenRC.
func lookOpenRC(name string) (string, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("OpenRC's %s was not found; is this Alpine with OpenRC?", name)
	}
	return path, nil
}

func (o Options) withDefaults() Options {
	if o.Root == "" {
		o.Root = "/"
	}
	if o.Out == nil {
		o.Out = io.Discard
	}
	if o.Run == nil {
		out := o.Out
		o.Run = func(name string, args ...string) error {
			path, err := lookOpenRC(name)
			if err != nil {
				return err
			}
			cmd := exec.Command(path, args...)
			cmd.Stdout, cmd.Stderr = out, out
			return cmd.Run()
		}
	}
	return o
}

func (o Options) path(rel string) string { return filepath.Join(o.Root, rel) }

// Install writes the init script (always), the conf.d file and the configuration (only if they are missing),
// creates the state directory and adds the service to the default runlevel.
func Install(o Options) error {
	if o.Run == nil { // fail before anything is written if OpenRC is missing
		if _, err := lookOpenRC("rc-update"); err != nil {
			return err
		}
	}
	o = o.withDefaults()
	if !safeBinary.MatchString(o.Binary) {
		return fmt.Errorf("binary path %q must be absolute and contain only letters, digits and ./_+-", o.Binary)
	}
	if err := o.write(InitScript, strings.ReplaceAll(initScript, "@COMMAND@", o.Binary), 0o755, true); err != nil {
		return err
	}
	if err := o.write(ConfD, confD, 0o644, false); err != nil {
		return err
	}
	if err := mkdirNew(o.path(ConfigDir), 0o755); err != nil {
		return err
	}
	if err := o.write(ConfigFile, DefaultConfig, 0o600, false); err != nil {
		return err
	}
	if err := mkdir(o.path(StateDir), 0o711); err != nil {
		return err
	}
	if err := o.Run("rc-update", "add", "edelweiss", "default"); err != nil {
		return fmt.Errorf("rc-update add edelweiss default: %w", err)
	}
	fmt.Fprintln(o.Out, "edelweiss is installed as an OpenRC service. Start it with: rc-service edelweiss start")
	return nil
}

// Uninstall stops the service if it runs, removes it from the default runlevel and deletes the init script. The
// conf.d file, the configuration and the state directory stay, so that a later Install picks up where the administrator
// left off. With purge it also deletes the conf.d file, the configuration and the state directory (installed
// extensions, the TLS certificate). Nothing is deleted if the service cannot be stopped.
func Uninstall(o Options, purge bool) error {
	o = o.withDefaults()
	if _, err := os.Stat(o.path(InitScript)); err == nil {
		if err := o.Run("rc-service", "--ifstarted", "edelweiss", "stop"); err != nil {
			return fmt.Errorf("rc-service edelweiss stop: %w", err)
		}
		if err := o.Run("rc-update", "del", "edelweiss", "default"); err != nil {
			fmt.Fprintf(o.Out, "warning: rc-update del edelweiss default: %v\n", err)
		}
	}
	remove := []string{InitScript}
	if purge {
		remove = append(remove, ConfD, ConfigDir, StateDir)
	}
	for _, rel := range remove {
		if _, err := os.Lstat(o.path(rel)); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err := os.RemoveAll(o.path(rel)); err != nil {
			return err
		}
		fmt.Fprintf(o.Out, "removed /%s\n", rel)
	}
	return nil
}

// write creates rel with mode. An existing file is replaced only if replace is set; otherwise it is kept.
func (o Options) write(rel, content string, mode os.FileMode, replace bool) error {
	p := o.path(rel)
	if !replace {
		if _, err := os.Stat(p); err == nil {
			fmt.Fprintf(o.Out, "kept existing /%s\n", rel)
			return nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".new"
	done := false
	defer func() {
		if !done {
			_ = os.Remove(tmp)
		}
	}()
	if err := os.WriteFile(tmp, []byte(content), mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil { // WriteFile's mode is subject to the umask
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		return err
	}
	done = true
	fmt.Fprintf(o.Out, "wrote /%s\n", rel)
	return nil
}

// mkdirNew creates dir with mode unless it exists; an existing directory keeps the mode an administrator gave it.
func mkdirNew(dir string, mode os.FileMode) error {
	if _, err := os.Stat(dir); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return mkdir(dir, mode)
}

func mkdir(dir string, mode os.FileMode) error {
	if err := os.MkdirAll(dir, mode); err != nil {
		return err
	}
	return os.Chmod(dir, mode)
}
