// Package selfupdate implements the bytes-dns self-update command.
//
// The Updater fetches the latest release from the bytes-commerce/bytes-dns
// GitHub repository, verifies the asset, and atomically replaces the
// running binary.
package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	githubAPIBase  = "https://api.github.com"
	requestTimeout = 10 * time.Second

	acceptHeader = "application/vnd.github+json"
	userAgentFmt = "bytes-dns/%s"
)

// ErrNotRoot is returned when the updater is run without root privileges.
var ErrNotRoot = errors.New("updater must be run as root")

// Release is a single GitHub release.
type Release struct {
	Tag    string
	Assets []Asset
}

// Asset is a single downloadable file attached to a release.
type Asset struct {
	Name string
	URL  string
}

// Updater performs a self-update.
type Updater struct {
	Owner     string
	Repo      string
	Version   string
	BaseURL   string // override for tests
	BinaryDir string // directory containing the running binary
	User      string // user the timer runs as (template instance)

	// Hooks for testing. Defaults call the real OS / network.
	RootCheck   func() error
	httpGet     func(ctx context.Context, url, accept, userAgent string) ([]byte, error)
	runCommand  func(ctx context.Context, name string, args ...string) (string, error)
	osRename    func(oldPath, newPath string) error
}

// Options configures a new Updater.
type Options struct {
	RepoOwner string
	RepoName  string
	Version   string
	BaseURL   string
	BinaryDir string
	User      string

	// httpGet overrides the HTTP GET hook (for tests). When nil,
	// defaultHTTPGet is used.
	httpGet func(ctx context.Context, url, accept, userAgent string) ([]byte, error)
}

// New returns an Updater configured for the bytes-commerce/bytes-dns repo.
func New(opts Options) *Updater {
	httpGet := opts.httpGet
	if httpGet == nil {
		httpGet = defaultHTTPGet
	}
	return &Updater{
		Owner:      opts.RepoOwner,
		Repo:       opts.RepoName,
		Version:    opts.Version,
		BaseURL:    opts.BaseURL,
		BinaryDir:  opts.BinaryDir,
		User:       opts.User,
		RootCheck:  defaultRootCheck,
		httpGet:    httpGet,
		runCommand: defaultRunCommand,
		osRename:   defaultOsRename,
	}
}

// Latest fetches the latest release from GitHub.
func (u *Updater) Latest(ctx context.Context) (*Release, error) {
	base := u.BaseURL
	if base == "" {
		base = githubAPIBase
	}
	endpoint := fmt.Sprintf("%s/repos/%s/%s/releases/latest", base, u.Owner, u.Repo)
	accept := acceptHeader
	ua := fmt.Sprintf(userAgentFmt, u.Version)

	body, err := u.httpGet(ctx, endpoint, accept, ua)
	if err != nil {
		return nil, fmt.Errorf("fetching latest release: %w", err)
	}

	var raw struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decoding release JSON: %w", err)
	}

	rel := &Release{
		Tag: strings.TrimPrefix(raw.TagName, "v"),
	}
	for _, a := range raw.Assets {
		rel.Assets = append(rel.Assets, Asset{Name: a.Name, URL: a.URL})
	}
	return rel, nil
}

func defaultRootCheck() error {
	if os.Getuid() != 0 {
		return ErrNotRoot
	}
	return nil
}

func defaultHTTPGet(ctx context.Context, url, accept, userAgent string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", userAgent)

	client := &http.Client{Timeout: requestTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("GitHub API rate limit exceeded (HTTP 403)")
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("repository or release not found (HTTP 404)")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("unexpected HTTP %d from GitHub", resp.StatusCode)
	}

	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

func defaultRunCommand(ctx context.Context, name string, args ...string) (string, error) {
	// Implemented in Task 4 (used by Pre-flight).
	return "", nil
}

func defaultOsRename(oldPath, newPath string) error {
	// Implemented in Task 5 (used by Apply).
	return nil
}
