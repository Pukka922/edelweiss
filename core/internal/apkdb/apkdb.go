// Package apkdb tells which Alpine packages are installed, from apk's database of installed packages.
package apkdb

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"sync"
	"time"
)

// DefaultPath is where apk keeps its database of installed packages.
const DefaultPath = "/lib/apk/db/installed"

// DB answers which packages are installed. It reads the database again only when its modification time or size has
// changed. It is safe for concurrent use.
type DB struct {
	path string

	mu    sync.Mutex
	mod   time.Time
	size  int64
	names map[string]bool // nil until the database has been read
}

// New returns a DB for the database at path (DefaultPath on Alpine).
func New(path string) *DB { return &DB{path: path} }

// Missing returns the names in want that no installed package has as its name or provides, in the order of want, or
// nil if all are installed. Without a database (not Alpine) every name is missing; other errors reading it are
// returned. An empty want never reads the database.
func (d *DB) Missing(want []string) ([]string, error) {
	if len(want) == 0 {
		return nil, nil
	}
	names, err := d.installed()
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, n := range want {
		if !names[n] {
			missing = append(missing, n)
		}
	}
	return missing, nil
}

func (d *DB) installed() (map[string]bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	st, err := os.Stat(d.path)
	if errors.Is(err, fs.ErrNotExist) {
		d.names = nil
		return map[string]bool{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("apk database: %w", err)
	}
	if d.names != nil && st.ModTime().Equal(d.mod) && st.Size() == d.size {
		return d.names, nil
	}
	f, err := os.Open(d.path)
	if err != nil {
		return nil, fmt.Errorf("apk database: %w", err)
	}
	defer f.Close()
	names, err := Parse(f)
	if err != nil {
		return nil, fmt.Errorf("apk database %s: %w", d.path, err)
	}
	d.names, d.mod, d.size = names, st.ModTime(), st.Size()
	return names, nil
}

// Parse reads apk's database of installed packages: the name of every package ("P:name") and every name a package
// provides ("p:name name=version …").
func Parse(r io.Reader) (map[string]bool, error) {
	names := map[string]bool{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024) // provides lines of big packages are long
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "P:"):
			if n := line[2:]; n != "" {
				names[n] = true
			}
		case strings.HasPrefix(line, "p:"):
			for _, f := range strings.Fields(line[2:]) {
				if n, _, _ := strings.Cut(f, "="); n != "" {
					names[n] = true
				}
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return names, nil
}
