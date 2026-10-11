// Package auth authenticates local Linux users against /etc/shadow.
package auth

import (
	"bufio"
	"io"
	"os"
	"strconv"
	"strings"
)

// ShadowEntry is the relevant part of an /etc/shadow line.
type ShadowEntry struct {
	Name string
	Hash string
	// Expire is the account expiration in days since 1970-01-01, -1 if none.
	// An invalid expiry field is stored as 0 (already expired).
	Expire int64
}

// ParseShadow parses shadow(5) content. Malformed lines are skipped. An
// unparsable account expiry field is treated as already expired (Expire 0).
func ParseShadow(r io.Reader) (map[string]ShadowEntry, error) {
	entries := map[string]ShadowEntry{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, ":")
		if len(f) < 2 || f[0] == "" {
			continue
		}
		e := ShadowEntry{Name: f[0], Hash: f[1], Expire: -1}
		if len(f) > 7 && f[7] != "" {
			// A malformed or invalid (negative, other than -1) expiry must not
			// silently mean "never expires": fail closed as already expired.
			if v, err := strconv.ParseInt(f[7], 10, 64); err == nil && v >= -1 {
				e.Expire = v
			} else {
				e.Expire = 0
			}
		}
		entries[e.Name] = e
	}
	return entries, sc.Err()
}

// readShadowFile opens and parses the shadow file at path.
func readShadowFile(path string) (map[string]ShadowEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ParseShadow(f)
}

// LookupShadow returns the entry for name from the shadow file at path.
func LookupShadow(path, name string) (ShadowEntry, bool, error) {
	entries, err := readShadowFile(path)
	if err != nil {
		return ShadowEntry{}, false, err
	}
	e, ok := entries[name]
	return e, ok, nil
}
