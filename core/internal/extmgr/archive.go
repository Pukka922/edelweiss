package extmgr

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/Pukka922/edelweiss/core/internal/github"
)

// ErrBadArchive: a .tar.gz release asset does not contain exactly one executable file.
var ErrBadArchive = errors.New("the archive must contain exactly one executable file")

// extractExecutable copies the single executable regular file of the gzip-compressed tar archive into dst (mode
// 0755). Directories, pax global headers and non-executable regular files (README, LICENSE) are skipped; links and
// other special entries are refused, and so is more than limit bytes of content.
func extractExecutable(archive, dst string, limit int64) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBadArchive, err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	found := ""
	var total int64
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("%w: %v", ErrBadArchive, err)
		}
		switch h.Typeflag {
		case tar.TypeDir, tar.TypeXGlobalHeader:
			continue
		case tar.TypeReg:
		default:
			return fmt.Errorf("%w: %q is not a regular file", ErrBadArchive, h.Name)
		}
		// Compared as h.Size > limit-total, not total+h.Size > limit: a header (PAX) can announce up to MaxInt64
		// bytes and the sum would wrap around. total never exceeds the positive limit, so the subtraction is safe.
		if h.Size > limit-total {
			return fmt.Errorf("%w: the archive content is larger than %d bytes", github.ErrTooLarge, limit)
		}
		total += h.Size
		if h.Mode&0o111 == 0 {
			continue
		}
		if found != "" {
			return fmt.Errorf("%w: found %q and %q", ErrBadArchive, found, h.Name)
		}
		found = h.Name
		if err := writeExecutable(tr, dst, h.Size); err != nil {
			return err
		}
	}
	if found == "" {
		return fmt.Errorf("%w: none found", ErrBadArchive)
	}
	return nil
}

func writeExecutable(r io.Reader, dst string, size int64) error {
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.CopyN(out, r, size); err != nil {
		out.Close()
		return fmt.Errorf("%w: %v", ErrBadArchive, err)
	}
	return out.Close()
}
