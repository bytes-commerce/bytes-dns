// Package selfupdate implements the bytes-dns self-update command.
//
// The Updater fetches the latest release from the bytes-commerce/bytes-dns
// GitHub repository, verifies the asset, and atomically replaces the
// running binary.
package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	RootCheck  func() error
	httpGet    func(ctx context.Context, url, accept, userAgent string, maxBytes int) ([]byte, error)
	runCommand func(ctx context.Context, name string, args ...string) (string, error)
	osRename   func(oldPath, newPath string) error
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
	httpGet func(ctx context.Context, url, accept, userAgent string, maxBytes int) ([]byte, error)

	// runCommand overrides command execution (for tests). When nil,
	// defaultRunCommand is used.
	runCommand func(ctx context.Context, name string, args ...string) (string, error)

	// osRename overrides file renaming (for tests). When nil, defaultOsRename is used.
	osRename func(oldPath, newPath string) error
}

// New returns an Updater configured for the bytes-commerce/bytes-dns repo.
func New(opts Options) *Updater {
	httpGet := opts.httpGet
	if httpGet == nil {
		httpGet = defaultHTTPGet
	}
	runCommand := opts.runCommand
	if runCommand == nil {
		runCommand = defaultRunCommand
	}
	osRename := opts.osRename
	if osRename == nil {
		osRename = defaultOsRename
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
		runCommand: runCommand,
		osRename:   osRename,
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

	body, err := u.httpGet(ctx, endpoint, accept, ua, 1<<20)
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

// assetForPlatform returns the release asset name for the given GOOS/GOARCH/GOARM.
func assetForPlatform(goos, goarch, goarm string) string {
	switch {
	case goos == "linux" && goarch == "amd64":
		return "bytes-dns-linux-amd64"
	case goos == "linux" && goarch == "arm64":
		return "bytes-dns-linux-arm64"
	case goos == "linux" && goarch == "arm" && goarm == "7":
		return "bytes-dns-linux-armv7"
	default:
		panic(fmt.Sprintf("no prebuilt binary for %s/%s (GOARM=%s); build from source", goos, goarch, goarm))
	}
}

// FindAsset returns the release asset matching the current platform.
func (u *Updater) FindAsset(rel *Release, goos, goarch, goarm string) (*Asset, error) {
	want := assetForPlatform(goos, goarch, goarm)
	for i := range rel.Assets {
		if rel.Assets[i].Name == want {
			return &rel.Assets[i], nil
		}
	}
	return nil, fmt.Errorf("asset %q not found in release %s", want, rel.Tag)
}

func defaultRootCheck() error {
	if os.Getuid() != 0 {
		return ErrNotRoot
	}
	return nil
}

func defaultHTTPGet(ctx context.Context, url, accept, userAgent string, maxBytes int) ([]byte, error) {
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

	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}
	return io.ReadAll(io.LimitReader(resp.Body, int64(maxBytes)))
}

// Preflight executes the staged binary and verifies that its --version output
// reports a non-empty version string. This is a sanity check before swapping
// the binary in place.
func (u *Updater) Preflight(ctx context.Context, binPath string) error {
	out, err := u.runCommand(ctx, binPath, "version")
	if err != nil {
		return fmt.Errorf("executing %s --version: %w", binPath, err)
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return fmt.Errorf("%s --version returned empty output", binPath)
	}
	// Sanity: the output should contain a digit (version number).
	hasDigit := false
	for _, r := range out {
		if r >= '0' && r <= '9' {
			hasDigit = true
			break
		}
	}
	if !hasDigit {
		return fmt.Errorf("version mismatch: %s --version output does not look like a version string: %q", binPath, out)
	}
	return nil
}

func defaultRunCommand(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// Apply atomically replaces the running binary with the staged one and
// restarts the systemd timer so the next run uses the new binary.
//
// The old binary is preserved at <dest>.old.<oldVer> for manual rollback.
func (u *Updater) Apply(ctx context.Context, stagedPath, oldVer string) error {
	dest := filepath.Join(u.BinaryDir, "bytes-dns")
	backup := filepath.Join(u.BinaryDir, fmt.Sprintf("bytes-dns.old.%s", oldVer))

	// If the staged path is the same as dest, nothing to do.
	if stagedPath == dest {
		return nil
	}

	// Remove any stale backup from a prior update.
	if _, err := os.Stat(backup); err == nil {
		_ = os.Remove(backup)
	}

	if err := u.osRename(dest, backup); err != nil {
		return fmt.Errorf("backing up old binary: %w", err)
	}
	if err := u.osRename(stagedPath, dest); err != nil {
		// Try to restore the old binary.
		_ = u.osRename(backup, dest)
		return fmt.Errorf("moving new binary into place: %w", err)
	}
	if err := os.Chmod(dest, 0o755); err != nil {
		return fmt.Errorf("chmod new binary: %w", err)
	}

	// Restart the systemd timer.
	timerUnit := fmt.Sprintf("bytes-dns@%s.timer", u.User)
	if _, err := u.runCommand(ctx, "systemctl", "restart", timerUnit); err != nil {
		return fmt.Errorf("restarting %s: %w", timerUnit, err)
	}
	return nil
}

func defaultOsRename(oldPath, newPath string) error {
	return os.Rename(oldPath, newPath)
}

// Action describes what an update did.
type Action string

const (
	ActionNoChange Action = "no_change"
	ActionUpdated  Action = "updated"
	ActionError    Action = "error"
)

// UpdateOptions configures a single Update call.
type UpdateOptions struct {
	Force bool // re-install even if version matches
	Check bool // dry-run: just compare versions
}

// Result describes the outcome of an Update or Check call.
type Result struct {
	Action          Action
	Current         string
	Latest          string
	Platform        string
	UpdateAvailable bool
}

// Check fetches the latest release and reports whether an update is available.
func (u *Updater) Check(ctx context.Context) (*Result, error) {
	rel, err := u.Latest(ctx)
	if err != nil {
		return nil, err
	}
	return &Result{
		Action:          ActionNoChange,
		Current:         u.Version,
		Latest:          rel.Tag,
		Platform:        fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
		UpdateAvailable: u.Version != rel.Tag,
	}, nil
}

// Update fetches the latest release and, when needed, downloads, verifies,
// stages, preflights, and atomically applies its platform asset.
func (u *Updater) Update(ctx context.Context, opts UpdateOptions) (*Result, error) {
	rel, err := u.Latest(ctx)
	if err != nil {
		return nil, err
	}

	result := &Result{
		Action:          ActionNoChange,
		Current:         u.Version,
		Latest:          rel.Tag,
		Platform:        fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
		UpdateAvailable: u.Version != rel.Tag,
	}
	if opts.Check || (u.Version == rel.Tag && !opts.Force) {
		return result, nil
	}

	asset, err := u.FindAsset(rel, runtime.GOOS, runtime.GOARCH, os.Getenv("GOARM"))
	if err != nil {
		return nil, err
	}
	body, err := u.Download(ctx, *asset)
	if err != nil {
		return nil, err
	}
	checksums, err := u.FetchChecksums(ctx, rel)
	if err != nil {
		return nil, err
	}
	if err := verifyChecksum(body, asset.Name, checksums); err != nil {
		return nil, err
	}

	stagedPath := filepath.Join(u.BinaryDir, "bytes-dns.new")
	if err := os.WriteFile(stagedPath, body, 0o644); err != nil {
		return nil, fmt.Errorf("staging new binary: %w", err)
	}
	defer os.Remove(stagedPath)

	if err := u.Preflight(ctx, stagedPath); err != nil {
		return nil, err
	}
	if err := u.Apply(ctx, stagedPath, u.Version); err != nil {
		return nil, err
	}

	result.Action = ActionUpdated
	return result, nil
}

// Download fetches the asset's bytes.
func (u *Updater) Download(ctx context.Context, asset Asset) ([]byte, error) {
	ua := fmt.Sprintf(userAgentFmt, u.Version)
	body, err := u.httpGet(ctx, asset.URL, "application/octet-stream", ua, 100<<20)
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w", asset.Name, err)
	}
	return body, nil
}

// FetchChecksums downloads and returns the checksums.txt for a release.
func (u *Updater) FetchChecksums(ctx context.Context, rel *Release) ([]byte, error) {
	for _, a := range rel.Assets {
		if a.Name == "checksums.txt" {
			ua := fmt.Sprintf(userAgentFmt, u.Version)
			body, err := u.httpGet(ctx, a.URL, "application/octet-stream", ua, 1<<20)
			if err != nil {
				return nil, fmt.Errorf("downloading %s: %w", a.Name, err)
			}
			return body, nil
		}
	}
	return nil, fmt.Errorf("checksums.txt not found in release %s", rel.Tag)
}

// verifyChecksum checks that body matches the entry in checksums for assetName.
// Expected format: each line is `<sha256>  <filename>` (one or more whitespace separators).
func verifyChecksum(body []byte, assetName string, checksums []byte) error {
	have := sha256.Sum256(body)
	haveHex := hex.EncodeToString(have[:])

	for _, line := range strings.Split(string(checksums), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if fields[1] != assetName {
			continue
		}
		if fields[0] != haveHex {
			return fmt.Errorf("checksum mismatch for %s: have %s, want %s", assetName, haveHex, fields[0])
		}
		return nil
	}
	return fmt.Errorf("checksum entry for %s not found in checksums.txt", assetName)
}
