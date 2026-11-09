package selfupdate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Pukka922/edelweiss/core/internal/github"
)

type fakeReleases struct {
	rel     *github.Release
	err     error
	lists   map[int64]string
	calls   int
	gotTag  string
	downErr error
}

func (f *fakeReleases) Release(_ context.Context, src github.Source) (*github.Release, error) {
	f.calls++
	f.gotTag = src.Tag
	return f.rel, f.err
}

func (f *fakeReleases) Download(_ context.Context, a github.Asset, w io.Writer, limit int64) (int64, error) {
	if f.downErr != nil {
		return 0, f.downErr
	}
	if limit != github.MaxChecksumsSize {
		return 0, fmt.Errorf("limit %d", limit)
	}
	n, err := io.Copy(w, bytes.NewBufferString(f.lists[a.ID]))
	return n, err
}

const digestA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const digestB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func release(tag string, assets ...github.Asset) *github.Release {
	return &github.Release{TagName: tag, HTMLURL: "https://example.test/rel", PublishedAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), Assets: assets}
}

func binAsset(id int64, version, arch string) github.Asset {
	return github.Asset{ID: id, Name: "edelweissd_" + version + "_linux_" + arch, Size: 1234}
}

func sumsAsset(id int64) github.Asset { return github.Asset{ID: id, Name: "checksums.txt"} }

func sumsFor(digest string, names ...string) string {
	var b strings.Builder
	for _, n := range names {
		fmt.Fprintf(&b, "%s  %s\n", digest, n)
	}
	return b.String()
}

func standard() *fakeReleases {
	return &fakeReleases{
		rel:   release("v0.3.1", binAsset(1, "0.3.1", "amd64"), binAsset(2, "0.3.1", "arm64"), sumsAsset(9)),
		lists: map[int64]string{9: sumsFor(digestA, "edelweissd_0.3.1_linux_amd64") + sumsFor(digestB, "edelweissd_0.3.1_linux_arm64")},
	}
}

func TestCheckOffersNewer(t *testing.T) {
	f := standard()
	o, err := Check(context.Background(), f, Source, "amd64", "0.3.0")
	if err != nil {
		t.Fatal(err)
	}
	if f.gotTag != "" {
		t.Errorf("tag = %q, want latest", f.gotTag)
	}
	if !o.Available || o.Current != "0.3.0" || o.Latest != "0.3.1" || o.Release != "v0.3.1" ||
		o.Asset != "edelweissd_0.3.1_linux_amd64" || o.Size != 1234 || o.SHA256 != digestA ||
		o.NotesURL != "https://example.test/rel" || o.Published.IsZero() || o.asset.ID != 1 {
		t.Errorf("offer = %+v", o)
	}
}

func TestCheckSameOrOlder(t *testing.T) {
	for _, cur := range []string{"0.3.1", "0.4.0", "1.0.0"} {
		o, err := Check(context.Background(), standard(), Source, "amd64", cur)
		if err != nil {
			t.Fatal(err)
		}
		if o.Available || o.Latest != "0.3.1" || o.Current != cur {
			t.Errorf("current %s: offer = %+v", cur, o)
		}
	}
}

func TestCheckDevBuildAsksNothing(t *testing.T) {
	f := standard()
	o, err := Check(context.Background(), f, Source, "amd64", "0.0.0-dev")
	if err != nil || o.Available || o.Current != "0.0.0-dev" || f.calls != 0 {
		t.Errorf("offer = %+v, err = %v, calls = %d", o, err, f.calls)
	}
}

func TestCheckSkipsDraftAndPrerelease(t *testing.T) {
	for _, mod := range []func(*github.Release){func(r *github.Release) { r.Draft = true }, func(r *github.Release) { r.Prerelease = true }} {
		f := standard()
		mod(f.rel)
		o, err := Check(context.Background(), f, Source, "amd64", "0.3.0")
		if err != nil || o.Available {
			t.Errorf("offer = %+v, err = %v", o, err)
		}
	}
}

func TestCheckRefusesTagMismatch(t *testing.T) {
	f := standard()
	f.rel.TagName = "v0.3.2"
	if _, err := Check(context.Background(), f, Source, "amd64", "0.3.0"); !errors.Is(err, ErrNoAsset) {
		t.Errorf("err = %v, want ErrNoAsset", err)
	}
}

func TestCheckPicksArch(t *testing.T) {
	o, err := Check(context.Background(), standard(), Source, "arm64", "0.3.0")
	if err != nil || o.Asset != "edelweissd_0.3.1_linux_arm64" || o.SHA256 != digestB {
		t.Errorf("offer = %+v, err = %v", o, err)
	}
	f := standard()
	f.rel.Assets = []github.Asset{binAsset(1, "0.3.1", "amd64"), sumsAsset(9)}
	if _, err := Check(context.Background(), f, Source, "arm64", "0.3.0"); !errors.Is(err, ErrNoAsset) {
		t.Errorf("err = %v, want ErrNoAsset", err)
	}
}

func TestCheckNeedsChecksums(t *testing.T) {
	f := standard()
	f.rel.Assets = []github.Asset{binAsset(1, "0.3.1", "amd64")}
	if _, err := Check(context.Background(), f, Source, "amd64", "0.3.0"); !errors.Is(err, ErrNoChecksums) {
		t.Errorf("no list: err = %v", err)
	}
	f = standard()
	f.lists[9] = sumsFor(digestA, "edelweissd_0.3.1_linux_arm64")
	if _, err := Check(context.Background(), f, Source, "amd64", "0.3.0"); !errors.Is(err, ErrNoChecksums) {
		t.Errorf("not listed: err = %v", err)
	}
	f = standard()
	for i := int64(10); i < 19; i++ {
		f.rel.Assets = append(f.rel.Assets, sumsAsset(i))
	}
	if _, err := Check(context.Background(), f, Source, "amd64", "0.3.0"); !errors.Is(err, ErrNoChecksums) {
		t.Errorf("too many lists: err = %v", err)
	}
}

func TestCheckChecksumsDisagree(t *testing.T) {
	f := standard()
	f.rel.Assets = append(f.rel.Assets, github.Asset{ID: 10, Name: "extra_checksums.txt"})
	f.lists[10] = sumsFor(digestB, "edelweissd_0.3.1_linux_amd64")
	if _, err := Check(context.Background(), f, Source, "amd64", "0.3.0"); !errors.Is(err, ErrChecksumsDisagree) {
		t.Errorf("err = %v, want ErrChecksumsDisagree", err)
	}
	f.lists[10] = sumsFor(digestA, "edelweissd_0.3.1_linux_amd64")
	if o, err := Check(context.Background(), f, Source, "amd64", "0.3.0"); err != nil || o.SHA256 != digestA {
		t.Errorf("agreeing lists: %+v, %v", o, err)
	}
}

func TestCheckGitHubErrorsPassThrough(t *testing.T) {
	for _, want := range []error{github.ErrRateLimited, github.ErrUnavailable} {
		f := &fakeReleases{err: fmt.Errorf("wrapped: %w", want)}
		if _, err := Check(context.Background(), f, Source, "amd64", "0.3.0"); !errors.Is(err, want) {
			t.Errorf("err = %v, want %v", err, want)
		}
	}
	f := standard()
	f.downErr = github.ErrUnavailable
	if _, err := Check(context.Background(), f, Source, "amd64", "0.3.0"); !errors.Is(err, github.ErrUnavailable) {
		t.Errorf("download err = %v", err)
	}
}

func TestCheckSkipsPrereleaseVersionWithoutFlag(t *testing.T) {
	f := standard()
	f.rel = release("v0.4.0-rc.1", binAsset(1, "0.4.0-rc.1", "amd64"), sumsAsset(9))
	f.lists[9] = sumsFor(digestA, "edelweissd_0.4.0-rc.1_linux_amd64")
	o, err := Check(context.Background(), f, Source, "amd64", "0.3.0")
	if err != nil || o.Available {
		t.Errorf("offer = %+v, err = %v", o, err)
	}
}

func TestCheckRefusesUnsizedAsset(t *testing.T) {
	for _, size := range []int64{0, -1} {
		f := standard()
		f.rel.Assets[0].Size = size
		if _, err := Check(context.Background(), f, Source, "amd64", "0.3.0"); !errors.Is(err, ErrNoAsset) {
			t.Errorf("size %d: err = %v, want ErrNoAsset", size, err)
		}
	}
}

func TestCheckUnknownInstalledVersionOffersNothing(t *testing.T) {
	for _, cur := range []string{"", "dev", "v0.3.0", "garbage"} {
		f := standard()
		o, err := Check(context.Background(), f, Source, "amd64", cur)
		if err != nil || o.Available || f.calls != 0 {
			t.Errorf("current %q: offer = %+v, err = %v, calls = %d", cur, o, err, f.calls)
		}
	}
}
