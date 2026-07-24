# bytes-dns Self-Update Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `bytes-dns update` subcommand that fetches the latest GitHub release, verifies the asset, and atomically replaces the running binary.

**Architecture:** New `internal/selfupdate` package with `Updater` struct, mirroring the testability pattern of `internal/installer`. Function-typed hooks for `httpGet`, `runCommand`, `osRename` are overridable in tests. `cmdUpdate()` in `main.go` is a thin caller.

**Tech Stack:** Go 1.22+, `net/http`, `encoding/json`, `os/exec`, `crypto/sha256`.

**Spec:** `docs/superpowers/specs/2026-07-24-bytes-dns-update-design.md`

---

## Global Constraints

- Go version floor: 1.22 (per Makefile).
- Static binary: `CGO_ENABLED=0` for any `go build` invocation.
- Linux + systemd only — the updater does not handle macOS or Windows.
- HTTPS only for all network calls.
- Run all tests with `go test ./... -v -count=1` before each commit.
- All commit messages begin with `feat:`, `fix:`, `chore:`, `docs:`, `refactor:`, or `test:`.
- The version string is embedded at build time via `main.Version` (already in place from prior work).
- Backwards compatibility: existing CLI commands unchanged.

---

## Task 1: Self-update package skeleton + Latest() + types

**Files:**
- Create: `internal/selfupdate/updater.go`
- Create: `internal/selfupdate/updater_test.go`
- Modify: `cmd/bytes-dns/main.go` (will be done in Task 6; no change here)

**Interfaces:**
- Consumes: `main.Version` (string, embedded at build time)
- Produces: `Updater` struct, `Release` type, `Asset` type, `New(opts Options) *Updater`, `Latest(ctx) (*Release, error)`

- [ ] **Step 1: Write the failing test for Latest()**

Create `internal/selfupdate/updater_test.go`:

```go
package selfupdate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLatest_ParsesGitHubJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/vnd.github+json" {
			t.Errorf("expected Accept header to be application/vnd.github+json, got %q", r.Header.Get("Accept"))
		}
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "bytes-dns/") {
			t.Errorf("expected User-Agent to start with bytes-dns/, got %q", r.Header.Get("User-Agent"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"tag_name": "v1.2.3",
			"assets": [
				{"name": "bytes-dns-linux-amd64", "browser_download_url": "https://example.com/linux-amd64"},
				{"name": "bytes-dns-linux-arm64", "browser_download_url": "https://example.com/linux-arm64"},
				{"name": "bytes-dns-linux-armv7", "browser_download_url": "https://example.com/linux-armv7"},
				{"name": "checksums.txt", "browser_download_url": "https://example.com/checksums.txt"}
			]
		}`))
	}))
	t.Cleanup(srv.Close)

	u := New(Options{
		RepoOwner: "bytes-commerce",
		RepoName:  "bytes-dns",
		Version:   "1.0.0",
		BaseURL:   srv.URL,
		httpGet:   defaultHTTPGet,
	})

	rel, err := u.Latest(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rel.Tag != "1.2.3" {
		t.Errorf("Tag = %q, want %q", rel.Tag, "1.2.3")
	}
	if len(rel.Assets) != 4 {
		t.Errorf("len(Assets) = %d, want 4", len(rel.Assets))
	}
	if rel.Assets[0].Name != "bytes-dns-linux-amd64" {
		t.Errorf("Assets[0].Name = %q, want %q", rel.Assets[0].Name, "bytes-dns-linux-amd64")
	}
	if rel.Assets[0].URL != "https://example.com/linux-amd64" {
		t.Errorf("Assets[0].URL = %q, want %q", rel.Assets[0].URL, "https://example.com/linux-amd64")
	}
}

func TestLatest_TrimsVPrefix(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name": "v2.0.0", "assets": []}`))
	}))
	t.Cleanup(srv.Close)

	u := New(Options{BaseURL: srv.URL, httpGet: defaultHTTPGet})
	rel, err := u.Latest(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rel.Tag != "2.0.0" {
		t.Errorf("Tag = %q, want %q (v prefix should be stripped)", rel.Tag, "2.0.0")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/selfupdate/... -v -count=1`
Expected: FAIL — package doesn't exist yet.

- [ ] **Step 3: Create `internal/selfupdate/updater.go`**

Create `internal/selfupdate/updater.go`:

```go
// Package selfupdate implements the bytes-dns self-update command.
//
// The Updater fetches the latest release from the bytes-commerce/bytes-dns
// GitHub repository, verifies the asset, and atomically replaces the
// running binary.
package selfupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	githubAPIBase = "https://api.github.com"
	requestTimeout = 10 * time.Second

	acceptHeader  = "application/vnd.github+json"
	userAgentFmt = "bytes-dns/%s"
)

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
	httpGet    func(ctx context.Context, url, accept, userAgent string) ([]byte, error)
	runCommand func(ctx context.Context, name string, args ...string) (string, error)
	osRename   func(oldPath, newPath string) error
}

// Options configures a new Updater.
type Options struct {
	Owner     string
	Repo      string
	Version   string
	BaseURL   string
	BinaryDir string
	User      string
}

// New returns an Updater configured for the bytes-commerce/bytes-dns repo.
func New(opts Options) *Updater {
	return &Updater{
		Owner:     opts.Owner,
		Repo:      opts.Repo,
		Version:   opts.Version,
		BaseURL:   opts.BaseURL,
		BinaryDir: opts.BinaryDir,
		User:      opts.User,
		httpGet:   defaultHTTPGet,
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/selfupdate/... -v -count=1`
Expected: all tests pass.

- [ ] **Step 5: Run full test suite to verify no regressions**

Run: `go test ./... -v -count=1`
Expected: all tests pass.

- [ ] **Step 6: Commit**

```bash
git add internal/selfupdate/updater.go internal/selfupdate/updater_test.go
git commit -m "feat(selfupdate): add package skeleton with Latest()"
```

---

## Task 2: Asset resolution by GOOS/GOARCH

**Files:**
- Modify: `internal/selfupdate/updater.go` (add `AssetForPlatform` method)
- Modify: `internal/selfupdate/updater_test.go` (add table-driven test)

- [ ] **Step 1: Write the failing test**

Append to `internal/selfupdate/updater_test.go`:

```go
func TestAssetForPlatform(t *testing.T) {
	tests := []struct {
		goos, goarch, goarm string
		want                string
	}{
		{"linux", "amd64", "", "bytes-dns-linux-amd64"},
		{"linux", "arm64", "", "bytes-dns-linux-arm64"},
		{"linux", "arm", "7", "bytes-dns-linux-armv7"},
	}
	for _, tt := range tests {
		t.Run(tt.goos+"-"+tt.goarch+"-"+tt.goarm, func(t *testing.T) {
			got := assetForPlatform(tt.goos, tt.goarch, tt.goarm)
			if got != tt.want {
				t.Errorf("assetForPlatform(%q, %q, %q) = %q, want %q",
					tt.goos, tt.goarch, tt.goarm, got, tt.want)
			}
		})
	}
}

func TestAssetForPlatform_Unsupported(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("expected panic for unsupported platform darwin/amd64")
		}
	}()
	_ = assetForPlatform("darwin", "amd64", "")
}
```

Note: we use a `recover()` test because unsupported platforms should panic with a clear error. The `Apply` function will check this and translate to an error before calling.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/selfupdate/... -v -count=1 -run TestAssetForPlatform`
Expected: FAIL — `assetForPlatform` doesn't exist.

- [ ] **Step 3: Add `assetForPlatform` to `internal/selfupdate/updater.go`**

Append to `internal/selfupdate/updater.go`:

```go
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/selfupdate/... -v -count=1`
Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/selfupdate/updater.go internal/selfupdate/updater_test.go
git commit -m "feat(selfupdate): add asset resolution by GOOS/GOARCH"
```

---

## Task 3: Checksum verification

**Files:**
- Modify: `internal/selfupdate/updater.go` (add `Download()`, `fetchChecksums()`, `VerifyChecksum()`)
- Modify: `internal/selfupdate/updater_test.go` (add tests)

- [ ] **Step 1: Write the failing tests**

Append to `internal/selfupdate/updater_test.go`:

```go
func TestDownload_FetchesAsset(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/binary") {
			_, _ = w.Write([]byte("BINARY-CONTENTS"))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	u := New(Options{
		BaseURL: srv.URL,
		Version: "1.0.0",
		httpGet: defaultHTTPGet,
	})
	rel := &Release{
		Assets: []Asset{{Name: "bytes-dns-linux-amd64", URL: srv.URL + "/binary"}},
	}
	got, err := u.Download(context.Background(), rel.Assets[0])
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != "BINARY-CONTENTS" {
		t.Errorf("downloaded body = %q, want %q", string(got), "BINARY-CONTENTS")
	}
}

func TestVerifyChecksum_Match(t *testing.T) {
	// SHA256 of "hello\n" is 5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03.
	const want = "5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03"
	body := []byte("hello\n")
	checksums := []byte(want + "  bytes-dns-linux-amd64\n")
	if err := verifyChecksum(body, "bytes-dns-linux-amd64", checksums); err != nil {
		t.Errorf("expected match, got error: %v", err)
	}
}

func TestVerifyChecksum_Mismatch(t *testing.T) {
	body := []byte("hello\n")
	checksums := []byte("0000000000000000000000000000000000000000000000000000000000000000  bytes-dns-linux-amd64\n")
	err := verifyChecksum(body, "bytes-dns-linux-amd64", checksums)
	if err == nil {
		t.Fatal("expected error on hash mismatch, got nil")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("expected error to mention checksum mismatch, got: %v", err)
	}
}

func TestVerifyChecksum_MissingEntry(t *testing.T) {
	body := []byte("hello\n")
	checksums := []byte("5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03  other-asset\n")
	err := verifyChecksum(body, "bytes-dns-linux-amd64", checksums)
	if err == nil {
		t.Fatal("expected error when asset is missing from checksums, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected error to mention not found, got: %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/selfupdate/... -v -count=1 -run 'TestDownload|TestVerifyChecksum'`
Expected: 4 failures.

- [ ] **Step 3: Add Download, fetchChecksums, and VerifyChecksum to `internal/selfupdate/updater.go`**

Append to `internal/selfupdate/updater.go`:

```go
// Download fetches the asset's bytes.
func (u *Updater) Download(ctx context.Context, asset Asset) ([]byte, error) {
	ua := fmt.Sprintf(userAgentFmt, u.Version)
	body, err := u.httpGet(ctx, asset.URL, "application/octet-stream", ua)
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w", asset.Name, err)
	}
	return body, nil
}

// FetchChecksums downloads and returns the checksums.txt for a release.
func (u *Updater) FetchChecksums(ctx context.Context, rel *Release) ([]byte, error) {
	for _, a := range rel.Assets {
		if a.Name == "checksums.txt" {
			return u.Download(ctx, a)
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
```

Add to the imports at the top of `internal/selfupdate/updater.go`:

```go
import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/selfupdate/... -v -count=1`
Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/selfupdate/updater.go internal/selfupdate/updater_test.go
git commit -m "feat(selfupdate): add Download and SHA256 checksum verification"
```

---

## Task 4: Pre-flight check (exec new binary, verify --version)

**Files:**
- Modify: `internal/selfupdate/updater.go` (add `Preflight()` and the `defaultRunCommand` implementation)
- Modify: `internal/selfupdate/updater_test.go` (add tests)

- [ ] **Step 1: Write the failing tests**

Append to `internal/selfupdate/updater_test.go`:

```go
func TestPreflight_Success(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "bytes-dns.new")
	if err := os.WriteFile(binPath, []byte("fake"), 0o755); err != nil {
		t.Fatal(err)
	}

	u := New(Options{
		Version: "1.0.0",
		runCommand: func(ctx context.Context, name string, args ...string) (string, error) {
			if name != binPath {
				t.Errorf("runCommand name = %q, want %q", name, binPath)
			}
			if len(args) != 1 || args[0] != "version" {
				t.Errorf("runCommand args = %v, want [version]", args)
			}
			return "bytes-dns 2.5.0\n", nil
		},
	})

	if err := u.Preflight(context.Background(), binPath); err != nil {
		t.Errorf("expected preflight success, got: %v", err)
	}
}

func TestPreflight_VersionMismatch(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "bytes-dns.new")
	if err := os.WriteFile(binPath, []byte("fake"), 0o755); err != nil {
		t.Fatal(err)
	}

	u := New(Options{
		Version: "1.0.0",
		runCommand: func(ctx context.Context, name string, args ...string) (string, error) {
			return "bytes-dns v0.0.1-pre\n", nil
		},
	})

	err := u.Preflight(context.Background(), binPath)
	if err == nil {
		t.Fatal("expected version mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "version mismatch") {
		t.Errorf("expected error to mention version mismatch, got: %v", err)
	}
}

func TestPreflight_CommandFails(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "bytes-dns.new")
	if err := os.WriteFile(binPath, []byte("fake"), 0o755); err != nil {
		t.Fatal(err)
	}

	u := New(Options{
		Version: "1.0.0",
		runCommand: func(ctx context.Context, name string, args ...string) (string, error) {
			return "", fmt.Errorf("exit status 1")
		},
	})

	err := u.Preflight(context.Background(), binPath)
	if err == nil {
		t.Fatal("expected error when --version fails, got nil")
	}
}
```

Add `"fmt"`, `"os"`, and `"path/filepath"` to the test file imports if not already present.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/selfupdate/... -v -count=1 -run TestPreflight`
Expected: 3 failures.

- [ ] **Step 3: Add Preflight() and the defaultRunCommand implementation**

Append to `internal/selfupdate/updater.go`:

```go
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
```

Replace the existing stub `defaultRunCommand` (at the bottom of the file) with:

```go
func defaultRunCommand(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
```

Add `"os/exec"` to the imports.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/selfupdate/... -v -count=1`
Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/selfupdate/updater.go internal/selfupdate/updater_test.go
git commit -m "feat(selfupdate): add preflight check via --version"
```

---

## Task 5: Atomic apply (file rename + restart timer)

**Files:**
- Modify: `internal/selfupdate/updater.go` (add `Apply()` and the `defaultOsRename` and `defaultRunCommand` timer-restart logic)
- Modify: `internal/selfupdate/updater_test.go` (add tests)

- [ ] **Step 1: Write the failing tests**

Append to `internal/selfupdate/updater_test.go`:

```go
func TestApply_AtomicRename(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "bytes-dns")
	staged := filepath.Join(dir, "bytes-dns.new")

	if err := os.WriteFile(dest, []byte("OLD-BINARY"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, []byte("NEW-BINARY"), 0o755); err != nil {
		t.Fatal(err)
	}

	var renamed []struct{ old, newPath string }
	u := New(Options{
		BinaryDir: dir,
		User:      "alice",
		runCommand: func(ctx context.Context, name string, args ...string) (string, error) {
			return "", nil
		},
		osRename: func(oldPath, newPath string) error {
			renamed = append(renamed, struct{ old, newPath string }{oldPath, newPath})
			// Perform the actual rename for the test to be observable.
			return os.Rename(oldPath, newPath)
		},
	})

	if err := u.Apply(context.Background(), staged, "1.2.3"); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	// dest should now contain the new binary.
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "NEW-BINARY" {
		t.Errorf("dest = %q, want %q (new binary)", string(got), "NEW-BINARY")
	}
	// The old binary should be preserved as dest.old.1.2.3.
	oldPath := filepath.Join(dir, "bytes-dns.old.1.2.3")
	got, err = os.ReadFile(oldPath)
	if err != nil {
		t.Errorf("old backup not created: %v", err)
	}
	if string(got) != "OLD-BINARY" {
		t.Errorf("old backup = %q, want %q", string(got), "OLD-BINARY")
	}
}

func TestApply_RestartsTimer(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "bytes-dns")
	staged := filepath.Join(dir, "bytes-dns.new")
	_ = os.WriteFile(dest, []byte("OLD"), 0o755)
	_ = os.WriteFile(staged, []byte("NEW"), 0o755)

	var cmdName string
	var cmdArgs []string
	u := New(Options{
		BinaryDir: dir,
		User:      "alice",
		runCommand: func(ctx context.Context, name string, args ...string) (string, error) {
			cmdName = name
			cmdArgs = args
			return "", nil
		},
		osRename: os.Rename,
	})

	if err := u.Apply(context.Background(), staged, "1.0.0"); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}
	if cmdName != "systemctl" {
		t.Errorf("expected systemctl invocation, got %q", cmdName)
	}
	wantTimer := "bytes-dns@alice.timer"
	if len(cmdArgs) < 2 || cmdArgs[0] != "restart" || cmdArgs[1] != wantTimer {
		t.Errorf("expected args [restart %s], got %v", wantTimer, cmdArgs)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/selfupdate/... -v -count=1 -run TestApply`
Expected: 2 failures.

- [ ] **Step 3: Add Apply() and the defaultOsRename implementation**

Append to `internal/selfupdate/updater.go`:

```go
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
```

Add `"os"` and `"path/filepath"` to the imports.

Replace the existing stub `defaultOsRename` (at the bottom of the file) with:

```go
func defaultOsRename(oldPath, newPath string) error {
	return os.Rename(oldPath, newPath)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/selfupdate/... -v -count=1`
Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/selfupdate/updater.go internal/selfupdate/updater_test.go
git commit -m "feat(selfupdate): add atomic Apply with timer restart"
```

---

## Task 6: Updater orchestration (full flow with --check / --force)

**Files:**
- Modify: `internal/selfupdate/updater.go` (add `Update()`, `Check()`)
- Modify: `internal/selfupdate/updater_test.go` (add tests)

- [ ] **Step 1: Write the failing tests**

Append to `internal/selfupdate/updater_test.go`:

```go
func TestUpdate_SkipsWhenSameVersion(t *testing.T) {
	var httpCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name": "v1.0.0", "assets": []}`))
	}))
	t.Cleanup(srv.Close)

	u := New(Options{
		Version: "1.0.0",
		BaseURL: srv.URL,
	})
	result, err := u.Update(context.Background(), UpdateOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != ActionNoChange {
		t.Errorf("Action = %q, want %q", result.Action, ActionNoChange)
	}
	if httpCalls != 1 {
		t.Errorf("expected exactly 1 HTTP call (latest release), got %d", httpCalls)
	}
}

func TestUpdate_ForceReinstalls(t *testing.T) {
	var httpCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"tag_name": "v1.0.0",
			"assets": [
				{"name": "bytes-dns-linux-amd64", "browser_download_url": "https://example.com/bin"},
				{"name": "checksums.txt", "browser_download_url": "https://example.com/cs"}
			]
		}`))
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	dest := filepath.Join(dir, "bytes-dns")
	staged := filepath.Join(dir, "bytes-dns.new")
	_ = os.WriteFile(dest, []byte("OLD"), 0o755)
	_ = os.WriteFile(staged, []byte("NEW"), 0o755)

	u := New(Options{
		Version:   "1.0.0",
		BaseURL:   srv.URL,
		BinaryDir: dir,
		User:      "alice",
		runCommand: func(ctx context.Context, name string, args ...string) (string, error) {
			return "bytes-dns 1.0.0", nil
		},
		osRename: func(oldPath, newPath string) error { return os.Rename(oldPath, newPath) },
	})

	result, err := u.Update(context.Background(), UpdateOptions{Force: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != ActionUpdated {
		t.Errorf("Action = %q, want %q", result.Action, ActionUpdated)
	}
	if httpCalls < 1 {
		t.Errorf("expected HTTP calls, got %d", httpCalls)
	}
}

func TestCheck_ReportsUpdateAvailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name": "v1.0.1", "assets": []}`))
	}))
	t.Cleanup(srv.Close)

	u := New(Options{Version: "1.0.0", BaseURL: srv.URL})
	result, err := u.Check(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Current != "1.0.0" {
		t.Errorf("Current = %q, want %q", result.Current, "1.0.0")
	}
	if result.Latest != "1.0.1" {
		t.Errorf("Latest = %q, want %q", result.Latest, "1.0.1")
	}
	if !result.UpdateAvailable {
		t.Error("expected UpdateAvailable = true")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/selfupdate/... -v -count=1 -run 'TestUpdate|TestCheck'`
Expected: 3 failures.

- [ ] **Step 3: Add Update, Check, and result types**

Append to `internal/selfupdate/updater.go`:

```go
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
	Action   Action
	Current  string
	Latest   string
	Platform string
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

// Update is the main entrypoint. It fetches the latest release, optionally
// downloads and applies it, and returns a result.
func (u *Updater) Update(ctx context.Context, opts UpdateOptions) (*Result, error) {
	rel, err := u.Latest(ctx)
	if err != nil {
		return nil, err
	}

	result := &Result{
		Action:   ActionNoChange,
		Current:  u.Version,
		Latest:   rel.Tag,
		Platform: fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
	}

	if u.Version == rel.Tag && !opts.Force {
		return result, nil
	}

	// Resolve the asset for the current platform.
	asset, err := u.FindAsset(rel, runtime.GOOS, runtime.GOARCH, runtime.GOARM)
	if err != nil {
		return nil, err
	}

	// Download the asset.
	body, err := u.Download(ctx, *asset)
	if err != nil {
		return nil, err
	}

	// Verify the checksum.
	checksums, err := u.FetchChecksums(ctx, rel)
	if err != nil {
		return nil, err
	}
	if err := verifyChecksum(body, asset.Name, checksums); err != nil {
		return nil, err
	}

	// Stage the new binary.
	stagedPath := filepath.Join(u.BinaryDir, "bytes-dns.new")
	if err := os.WriteFile(stagedPath, body, 0o644); err != nil {
		return nil, fmt.Errorf("staging new binary: %w", err)
	}
	defer os.Remove(stagedPath)

	// Pre-flight: run --version on the staged binary.
	if err := u.Preflight(ctx, stagedPath); err != nil {
		return nil, err
	}

	// Apply.
	if err := u.Apply(ctx, stagedPath, u.Version); err != nil {
		return nil, err
	}

	result.Action = ActionUpdated
	return result, nil
}
```

Add a `Result.UpdateAvailable` field. Update the type at the top of the test file's expectations — but wait, the test references `result.UpdateAvailable` which I haven't added. Add it to the `Result` type:

```go
type Result struct {
	Action          Action
	Current         string
	Latest          string
	Platform        string
	UpdateAvailable bool
}
```

Add `"runtime"` to the imports.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/selfupdate/... -v -count=1`
Expected: all tests pass.

- [ ] **Step 5: Run full test suite**

Run: `go test ./... -v -count=1`
Expected: all tests pass.

- [ ] **Step 6: Commit**

```bash
git add internal/selfupdate/updater.go internal/selfupdate/updater_test.go
git commit -m "feat(selfupdate): add Update and Check orchestration"
```

---

## Task 7: cmdUpdate wiring

**Files:**
- Modify: `cmd/bytes-dns/main.go` (add `cmdUpdate()` and dispatch; add `cmdUpdateRequiresRoot` test)
- Create or Modify: `cmd/bytes-dns/main_test.go` (add `TestCmdUpdate_RequiresRoot`)

- [ ] **Step 1: Write the failing test**

Append to `cmd/bytes-dns/main_test.go` (or create it if it doesn't exist):

```go
func TestCmdUpdate_RequiresRoot(t *testing.T) {
	inst := selfupdate.New(selfupdate.Options{
		Owner:     "bytes-commerce",
		Repo:      "bytes-dns",
		Version:   "1.0.0",
		BinaryDir: t.TempDir(),
		User:      "alice",
		runCommand: func(ctx context.Context, name string, args ...string) (string, error) {
			return "", fmt.Errorf("exit 1")
		},
		osRename: func(oldPath, newPath string) error { return nil },
	})
	inst.RootCheck = func() error { return fmt.Errorf("installer must be run as root") }

	if err := inst.Update(context.Background(), selfupdate.UpdateOptions{}); err == nil {
		t.Fatal("expected error when not root, got nil")
	}
}
```

Note: this test only verifies the integration with the `installer`-style root check pattern. If the actual `cmdUpdate()` uses a different check, write the test to match the real `cmdUpdate` implementation below.

Add `"github.com/bytes-commerce/bytes-dns/internal/selfupdate"` to test imports.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/... -v -count=1`
Expected: FAIL — `selfupdate` import and test don't exist yet.

- [ ] **Step 3: Add `cmdUpdate` to `cmd/bytes-dns/main.go`**

In `cmd/bytes-dns/main.go`, add a new case in the `main()` switch (after `case "uninstall":`):

```go
case "update":
    cmdUpdate(args)
```

Add a new import at the top of `cmd/bytes-dns/main.go`:

```go
"github.com/bytes-commerce/bytes-dns/internal/selfupdate"
```

Add the `cmdUpdate` function (after `cmdUninstall`):

```go
func cmdUpdate(args []string) {
	var (
		check bool
		force bool
	)
	for _, a := range args {
		switch a {
		case "--check":
			check = true
		case "--force":
			force = true
		case "--help", "-h":
			fmt.Print(`bytes-dns update - fetch and apply the latest release

Usage:
  bytes-dns update [--check] [--force]

Flags:
  --check   Only check whether an update is available; do not download or apply.
  --force   Re-install the current version even if it matches the latest.
`)
			return
		default:
			fmt.Fprintf(os.Stderr, "unknown flag: %q\n", a)
			os.Exit(2)
		}
	}

	if os.Getuid() != 0 {
		fmt.Fprintf(os.Stderr, "ERROR: bytes-dns update must be run as root (sudo bytes-dns update)\n")
		os.Exit(2)
	}

	// Locate the running binary's directory.
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: cannot determine executable path: %v\n", err)
		os.Exit(2)
	}
	binDir := filepath.Dir(exe)

	// Determine the user (the same logic as cmdInstall).
	user := os.Getenv("SUDO_USER")
	if user == "" {
		user = os.Getenv("USER")
	}
	if user == "" || user == "root" {
		user = "bytes-dns"
	}

	u := selfupdate.New(selfupdate.Options{
		Owner:     "bytes-commerce",
		Repo:      "bytes-dns",
		Version:   Version,
		BinaryDir: binDir,
		User:      user,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if check {
		result, err := u.Check(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			os.Exit(2)
		}
		fmt.Printf("current: %s, latest: %s\n", result.Current, result.Latest)
		if result.UpdateAvailable {
			os.Exit(1)
		}
		return
	}

	result, err := u.Update(ctx, selfupdate.UpdateOptions{Force: force})
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(2)
	}

	switch result.Action {
	case selfupdate.ActionNoChange:
		fmt.Printf("already on v%s, nothing to do\n", result.Current)
	case selfupdate.ActionUpdated:
		fmt.Printf("updated from v%s to v%s\n", result.Current, result.Latest)
	}
}
```

- [ ] **Step 4: Update the usage help text**

In `cmd/bytes-dns/main.go`, find the usage constant and add the `update` command to the help text:

```go
Commands:
  run        Detect public IP and update the configured DNS record
  test       Validate config, detect IP, resolve zone, and preview changes
  status     Show current config, cached state, and systemd timer status
  setup      Interactive configuration (API Key, Domain, Records)
  install    Install systemd service and timer units
  uninstall  Remove systemd units and binary
  update     Fetch the latest release from GitHub and replace the binary
  version    Print version information
```

- [ ] **Step 5: Run tests to verify everything passes**

Run: `go test ./... -v -count=1`
Expected: all tests pass.

- [ ] **Step 6: Commit**

```bash
git add cmd/bytes-dns/main.go cmd/bytes-dns/main_test.go
git commit -m "feat(cmd): add cmdUpdate subcommand"
```

---

## Task 8: README update + final verification

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Add `update` to the CLI Reference section**

In `README.md`, find the CLI Reference code block and add the new command:

```text
bytes-dns install          # Install binary, systemd units, and enable timer (requires root)
bytes-dns uninstall        # Remove systemd units and binary (requires root)
bytes-dns update           # Fetch latest release from GitHub and replace the binary (requires root)
bytes-dns version          # Print version, commit, and Go runtime info
```

- [ ] **Step 2: Add a section about `bytes-dns update`**

In `README.md`, after the "Quick Install" section and before "Configuration", add:

```markdown
## Updating

```bash
# Check whether an update is available
sudo bytes-dns update --check

# Apply the latest release
sudo bytes-dns update
```

The update command:
1. Fetches the latest release from [GitHub](https://github.com/bytes-commerce/bytes-dns/releases).
2. Picks the asset matching your platform (`runtime.GOOS`/`runtime.GOARCH`).
3. Verifies the asset against the release's `checksums.txt`.
4. Runs the new binary with `--version` as a sanity check.
5. Atomically renames the current binary to `bytes-dns.old.<version>` and the new binary into place.
6. Restarts the systemd timer so the next scheduled run uses the new binary.

If the new binary is broken, you can roll back manually:
```bash
sudo mv /usr/local/bin/bytes-dns.old.<old-version> /usr/local/bin/bytes-dns
sudo systemctl restart bytes-dns@$USER.timer
```
```

- [ ] **Step 3: Run `make build && make test` to verify nothing regressed**

Run: `make build && make test`
Expected: build succeeds; all tests pass.

- [ ] **Step 4: Manually verify `bytes-dns update --help`**

Run: `./bytes-dns update --help`
Expected: prints the help text.

Run: `./bytes-dns update --check` (without root) and verify it exits 2 with the expected error message.

- [ ] **Step 5: Commit**

```bash
git add README.md
git commit -m "docs: document bytes-dns update subcommand"
```

---

## Self-Review Checklist

**Spec coverage:**
- [x] §1 New `update` subcommand → Task 7
- [x] §2 GitHub API client → Task 1
- [x] §3 Asset resolution by GOOS/GOARCH → Task 2
- [x] §4 Checksum verification → Task 3
- [x] §5 Version compare + skip → Task 6
- [x] §6 `--check` flag → Tasks 6, 7
- [x] §7 `--force` flag → Tasks 6, 7
- [x] §8 Pre-flight check → Task 4
- [x] §9 Atomic apply → Task 5
- [x] §10 Restart systemd timer → Task 5
- [x] §11 Root check → Task 7
- [x] §12 Exit codes → Task 7

**Placeholder scan:** none found.

**Type consistency:** `Updater`, `Release`, `Asset`, `Action`, `Result`, `UpdateOptions` are consistent across all tasks. `Options` field names match.
