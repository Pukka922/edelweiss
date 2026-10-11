package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
)

var (
	// ErrReleaseNotFound: the repository does not exist, has no published release, or not the requested tag.
	ErrReleaseNotFound = errors.New("release not found")
	// ErrRateLimited: the API's request limit is exhausted.
	ErrRateLimited = errors.New("GitHub API rate limit exceeded")
	// ErrUnavailable: the API could not be reached or answered with an unexpected error.
	ErrUnavailable = errors.New("GitHub is unavailable")
	// ErrTooLarge: a download exceeds the allowed size.
	ErrTooLarge = errors.New("download exceeds the size limit")
)

const maxReleaseJSON = 5 << 20

// Release is the part of a GitHub release edelweiss uses.
type Release struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	HTMLURL     string    `json:"html_url"`
	PublishedAt time.Time `json:"published_at"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	Assets      []Asset   `json:"assets"`
}

// Asset is a file attached to a release. URL is its API endpoint, downloaded with Accept: application/octet-stream.
type Asset struct {
	ID                 int64  `json:"id"`
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	URL                string `json:"url"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// Client talks to the GitHub REST API or a compatible server.
type Client struct {
	APIURL    string // e.g. https://api.github.com
	Token     string // optional; sent only to the host of APIURL
	UserAgent string
	HTTP      *http.Client
}

// NewClient returns a client with transport timeouts; the overall duration of a request is bounded by the caller's
// context (a download may take minutes). The Authorization header never follows a redirect to another host, port or
// scheme.
func NewClient(apiURL, token, userAgent string) *Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSHandshakeTimeout = 10 * time.Second
	tr.ResponseHeaderTimeout = 30 * time.Second
	return &Client{
		APIURL: strings.TrimRight(apiURL, "/"), Token: token, UserAgent: userAgent,
		HTTP: &http.Client{Transport: tr, CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			if req.URL.Host != via[0].URL.Host || req.URL.Scheme != via[0].URL.Scheme {
				req.Header.Del("Authorization")
			}
			return nil
		}},
	}
}

// Release returns the latest release of src, or the release tagged src.Tag.
func (c *Client) Release(ctx context.Context, src Source) (*Release, error) {
	if err := src.Validate(); err != nil {
		return nil, err
	}
	p := "/repos/" + url.PathEscape(src.Owner) + "/" + url.PathEscape(src.Repo) + "/releases/"
	if src.Tag == "" {
		p += "latest"
	} else {
		p += "tags/" + url.PathEscape(src.Tag)
	}
	resp, err := c.get(ctx, c.APIURL+p, "application/vnd.github+json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := checkStatus(resp, src); err != nil {
		return nil, err
	}
	var rel Release
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxReleaseJSON)).Decode(&rel); err != nil {
		return nil, fmt.Errorf("%w: invalid release JSON: %v", ErrUnavailable, err)
	}
	if rel.TagName == "" {
		return nil, fmt.Errorf("%w: release without tag_name", ErrUnavailable)
	}
	return &rel, nil
}

// Download writes the asset to w and returns the number of bytes written. It fails with ErrTooLarge if the release
// announces more than limit bytes or more arrive. The limit must be positive.
func (c *Client) Download(ctx context.Context, a Asset, w io.Writer, limit int64) (int64, error) {
	if limit <= 0 {
		return 0, fmt.Errorf("download %q: the size limit must be positive, got %d", a.Name, limit)
	}
	if a.Size > limit {
		return 0, fmt.Errorf("%w: %q has %d bytes, the limit is %d", ErrTooLarge, a.Name, a.Size, limit)
	}
	target := a.URL
	if target == "" {
		target = a.BrowserDownloadURL
	}
	if target == "" {
		return 0, fmt.Errorf("%w: asset %q has no download URL", ErrUnavailable, a.Name)
	}
	resp, err := c.get(ctx, target, "application/octet-stream")
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("%w: download %q: HTTP %d", ErrUnavailable, a.Name, resp.StatusCode)
	}
	// Read one byte more than allowed to tell "exactly limit" from "more"; limit+1 would overflow at MaxInt64, where
	// more than limit bytes cannot arrive anyway.
	var body io.Reader = resp.Body
	if limit < math.MaxInt64 {
		body = io.LimitReader(resp.Body, limit+1)
	}
	n, err := io.Copy(w, body)
	if err != nil {
		return n, fmt.Errorf("download %q: %w", a.Name, err)
	}
	if n > limit {
		return n, fmt.Errorf("%w: %q is larger than %d bytes", ErrTooLarge, a.Name, limit)
	}
	return n, nil
}

func (c *Client) get(ctx context.Context, target, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	if c.Token != "" && c.sameHost(req.URL) {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		// %w twice: a context error (timeout, shutdown) stays visible to errors.Is next to ErrUnavailable
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return resp, nil
}

// sameHost reports whether u is on the API's host, port and scheme: only there the token may go, so it never travels
// in clear text to an http:// URL of an https API.
func (c *Client) sameHost(u *url.URL) bool {
	api, err := url.Parse(c.APIURL)
	return err == nil && api.Host == u.Host && api.Scheme == u.Scheme
}

func checkStatus(resp *http.Response, src Source) error {
	switch {
	case resp.StatusCode == http.StatusOK:
		return nil
	case resp.StatusCode == http.StatusNotFound:
		if src.Tag != "" {
			return fmt.Errorf("%w: %s has no release %s", ErrReleaseNotFound, src.Repository(), src.Tag)
		}
		return fmt.Errorf("%w: %s does not exist or has no published release", ErrReleaseNotFound, src.Repository())
	case resp.StatusCode == http.StatusTooManyRequests,
		resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") == "0":
		until := ""
		if reset, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			until = " until " + time.Unix(reset, 0).UTC().Format(time.RFC3339)
		}
		return fmt.Errorf("%w%s; set [github] token in the configuration to raise the limit", ErrRateLimited, until)
	default:
		err := fmt.Errorf("%w: %s: HTTP %d", ErrUnavailable, resp.Request.URL.Redacted(), resp.StatusCode)
		if msg := apiMessage(resp.Body); msg != "" {
			err = fmt.Errorf("%w %s", err, msg)
		}
		return err
	}
}

// maxMessageChars bounds the server's message that is quoted in an error.
const maxMessageChars = 200

// apiMessage extracts GitHub's {"message": ...} from an error answer. The text comes from the network, so it is cut
// to maxMessageChars characters and stripped of control characters (no terminal escapes or line breaks in our errors).
func apiMessage(r io.Reader) string {
	var body struct {
		Message string `json:"message"`
	}
	_ = json.NewDecoder(io.LimitReader(r, 64<<10)).Decode(&body)
	msg := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, body.Message)
	if runes := []rune(msg); len(runes) > maxMessageChars {
		msg = string(runes[:maxMessageChars])
	}
	return strings.TrimSpace(msg)
}
