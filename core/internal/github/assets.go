package github

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// AssetInfo is what an asset name says under the release convention edelweiss-ext-<id>_<version>_linux_<arch>
// (a raw binary) or the same name with .tar.gz (an archive with exactly one executable).
type AssetInfo struct {
	ID, Version, Arch string
	Archive           bool
}

var assetRe = regexp.MustCompile(`^edelweiss-ext-([a-z0-9][a-z0-9-]{1,31})_(\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?)_linux_(amd64|arm64)(\.tar\.gz)?$`)

// ParseAssetName reports whether name follows the convention and what it says.
func ParseAssetName(name string) (AssetInfo, bool) {
	m := assetRe.FindStringSubmatch(name)
	if m == nil {
		return AssetInfo{}, false
	}
	return AssetInfo{ID: m[1], Version: m[2], Arch: m[3], Archive: m[4] != ""}, true
}

// IsChecksums reports whether an asset is a checksum list: checksums.txt, or <prefix>checksums.txt as goreleaser
// names it.
func IsChecksums(name string) bool { return strings.HasSuffix(name, "checksums.txt") }

// MaxChecksumsSize bounds the download of a checksum list.
const MaxChecksumsSize = 1 << 20

// ParseChecksums reads a sha256sum listing ("<hex>  <name>" or "<hex> *<name>", blank lines allowed) into a map from
// file name to lower-case hex digest. A list longer than MaxChecksumsSize, or one that gives a name two different
// digests, is an error; the same digest twice is fine.
func ParseChecksums(r io.Reader) (map[string]string, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxChecksumsSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxChecksumsSize {
		return nil, fmt.Errorf("checksums list is larger than %d bytes", MaxChecksumsSize)
	}
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		sum, name, _ := strings.Cut(line, " ")
		name = strings.TrimPrefix(strings.TrimLeft(name, " "), "*")
		if _, err := hex.DecodeString(sum); err != nil || len(sum) != 64 || name == "" {
			return nil, fmt.Errorf("checksums line %d is not in sha256sum format", n)
		}
		sum = strings.ToLower(sum)
		if prev, dup := out[name]; dup && prev != sum {
			return nil, fmt.Errorf("checksums line %d: %q is listed with two different digests", n, name)
		}
		out[name] = sum
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
