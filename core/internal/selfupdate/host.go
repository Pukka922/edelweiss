// Package selfupdate updates edelweissd from its GitHub releases. This file holds the facts about the host that
// decide whether and how an update can work: the Alpine mode, the lbu list, the OpenRC tools and the eligibility.
package selfupdate

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/mod/semver"
	"golang.org/x/sys/unix"
)

// Mode is how Alpine runs: installed to disk (sys), from RAM (diskless) or from RAM with /var on disk (data).
type Mode string

const (
	Sys      Mode = "sys"
	Diskless Mode = "diskless"
	Data     Mode = "data"
)

// DetectMode reads <root>/proc/mounts: a / that is not tmpfs means sys, else a /var on a /dev/ device means data, else
// diskless. A mount point listed twice counts with its last line.
func DetectMode(root string) (Mode, error) {
	raw, err := os.ReadFile(filepath.Join(root, "proc/mounts"))
	if err != nil {
		return "", err
	}
	type mount struct{ device, fstype string }
	last := map[string]mount{}
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		last[unescape(f[1])] = mount{device: unescape(f[0]), fstype: f[2]}
	}
	if r, ok := last["/"]; !ok || r.fstype != "tmpfs" {
		return Sys, nil
	}
	if v, ok := last["/var"]; ok && strings.HasPrefix(v.device, "/dev/") {
		return Data, nil
	}
	return Diskless, nil
}

// unescape decodes the octal escapes of the proc files (\040 for a space); anything that is not a backslash and
// three octal digits of a byte stays as it is.
func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// parseLbuLine reads one line of the lbu list: "+path" or "-path" with the path relative to / (a leading or trailing
// slash is tolerated). ok is false for a blank line, a comment or a line in no known form. This is the only place that
// knows the format of the list.
func parseLbuLine(line string) (include bool, path string, ok bool) {
	line = strings.TrimSpace(line)
	if len(line) < 2 || (line[0] != '+' && line[0] != '-') {
		return false, "", false
	}
	path = strings.Trim(line[1:], "/")
	if path == "" {
		return false, "", false
	}
	return line[0] == '+', path, true
}

// LbuIncludes tells whether lbu saves path: <root>/etc/apk/protected_paths.d/lbu.list has a "+" line for the path or
// one of its parent directories, and no later "-" line for it. A missing list includes nothing.
func LbuIncludes(root, path string) (bool, error) {
	raw, err := os.ReadFile(filepath.Join(root, "etc/apk/protected_paths.d/lbu.list"))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	path = strings.Trim(path, "/")
	included := false
	for _, line := range strings.Split(string(raw), "\n") {
		include, entry, ok := parseLbuLine(line)
		if !ok {
			continue
		}
		if path == entry || strings.HasPrefix(path, entry+"/") {
			included = include
		}
	}
	return included, nil
}

// Tools are the programs the updater runs, by their path on the host; "" when a tool is not there.
type Tools struct{ RcService, Lbu string }

// binDirs are the directories FindTools looks in, in this order.
var binDirs = []string{"/usr/sbin", "/usr/bin", "/sbin", "/bin"}

// FindTools looks up each tool in /usr/sbin, /usr/bin, /sbin and /bin below root: the first that is an executable
// file there (a link to one counts). The paths it returns are those on the host, without root.
func FindTools(root string) Tools {
	find := func(name string) string {
		for _, dir := range binDirs {
			p := dir + "/" + name
			if st, err := os.Stat(filepath.Join(root, p)); err == nil && st.Mode().IsRegular() && st.Mode()&0o111 != 0 {
				return p
			}
		}
		return ""
	}
	return Tools{RcService: find("rc-service"), Lbu: find("lbu")}
}

// Reason is why no update is possible; ReasonNone means one is.
type Reason string

const (
	ReasonNone        Reason = ""
	ReasonDevMode     Reason = "dev_mode"
	ReasonDevBuild    Reason = "dev_build"
	ReasonNotService  Reason = "not_service"
	ReasonNoOpenRC    Reason = "no_openrc"
	ReasonNotWritable Reason = "not_writable"
	ReasonArch        Reason = "unsupported_arch"

	// The reasons below are the Service's, after those of Eligible.

	// ReasonUnknownMode: /proc/mounts cannot be read, so it is not known whether lbu must save the update.
	ReasonUnknownMode Reason = "unknown_mode"
	// ReasonNoLbu: the host runs from RAM (diskless or data), and there is no lbu to save the update.
	ReasonNoLbu Reason = "no_lbu"
	// ReasonListenAddress: the listen address gives the helper no URL to ask /api/health at (see HealthURL).
	ReasonListenAddress Reason = "listen_address"
)

// Host is what Eligible looks at: the root of the host files ("/" in production), the path of the running binary, its
// GOARCH, its version, whether it runs in dev mode and the parent process id.
type Host struct {
	Root, Binary, Arch, Version string
	Dev                         bool
	PPID                        int
}

var (
	supervisorLine = regexp.MustCompile(`(?m)^[ \t]*supervisor=supervise-daemon[ \t]*$`)
	commandLine    = regexp.MustCompile(`(?m)^[ \t]*command="([^"\n]*)"[ \t]*$`)
)

// Eligible returns the first reason in the order dev mode, dev build, architecture, OpenRC, service, writable
// directory that rules a self-update out, or ReasonNone.
func Eligible(h Host) Reason {
	switch {
	case h.Dev:
		return ReasonDevMode
	case !semver.IsValid("v"+h.Version) || h.Version == "0.0.0-dev":
		return ReasonDevBuild
	case h.Arch != "amd64" && h.Arch != "arm64":
		return ReasonArch
	case FindTools(h.Root).RcService == "":
		return ReasonNoOpenRC
	case !runsAsService(h):
		return ReasonNotService
	case unix.Access(filepath.Dir(h.Binary), unix.W_OK) != nil:
		return ReasonNotWritable
	}
	return ReasonNone
}

// runsAsService tells whether the running binary is the one the init script of edelweiss starts, under
// supervise-daemon: the script names supervise-daemon and this binary, and the parent process is supervise-daemon.
func runsAsService(h Host) bool {
	script, err := os.ReadFile(filepath.Join(h.Root, "etc/init.d/edelweiss"))
	if err != nil || !supervisorLine.Match(script) {
		return false
	}
	m := commandLine.FindSubmatch(script)
	if m == nil || string(m[1]) != h.Binary {
		return false
	}
	comm, err := os.ReadFile(filepath.Join(h.Root, "proc", strconv.Itoa(h.PPID), "comm"))
	// The kernel keeps 15 characters of a process name in comm, so a real supervise-daemon (16) shows as supervise-daemo.
	name := strings.TrimSpace(string(comm))
	return err == nil && (name == superviseDaemon || name == superviseDaemon[:15])
}

const superviseDaemon = "supervise-daemon"
