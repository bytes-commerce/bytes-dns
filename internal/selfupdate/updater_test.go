package selfupdate

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

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
	binary := []byte("NEW")
	assetName := assetForPlatform("linux", runtime.GOARCH, os.Getenv("GOARM"))
	checksum := fmt.Sprintf("%x  %s\n", sha256.Sum256(binary), assetName)

	var httpCalls int
	var serverURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpCalls++
		switch r.URL.Path {
		case "/repos///releases/latest":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{
				"tag_name": "v1.0.0",
				"assets": [
					{"name": "%s", "browser_download_url": %q},
					{"name": "checksums.txt", "browser_download_url": %q}
				]
			}`, assetName, serverURL+"/bin", serverURL+"/cs")
		case "/bin":
			_, _ = w.Write(binary)
		case "/cs":
			_, _ = w.Write([]byte(checksum))
		default:
			http.NotFound(w, r)
		}
	}))
	serverURL = srv.URL
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	dest := filepath.Join(dir, "bytes-dns")
	_ = os.WriteFile(dest, []byte("OLD"), 0o755)

	u := New(Options{
		Version:   "1.0.0",
		BaseURL:   srv.URL,
		BinaryDir: dir,
		User:      "alice",
		runCommand: func(ctx context.Context, name string, args ...string) (string, error) {
			if name == "systemctl" {
				return "", nil
			}
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
	if httpCalls != 3 {
		t.Errorf("expected 3 HTTP calls, got %d", httpCalls)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(binary) {
		t.Errorf("installed binary = %q, want %q", got, binary)
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
func TestLatest_ParsesGitHubJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/vnd.github+json" {
			t.Errorf("expected Accept header to be application/vnd.github+json, got %q", r.Header.Get("Accept"))
		}
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "bytes-dns/") {
			t.Errorf("expected User-Agent to start with bytes-dns/, got %q", r.Header.Get("User-Agent"))
		}
		_, _ = w.Write([]byte(`{"tag_name":"v1.2.3","assets":[{"name":"bytes-dns-linux-amd64","browser_download_url":"https://example.com/linux-amd64"},{"name":"bytes-dns-linux-arm64","browser_download_url":"https://example.com/linux-arm64"},{"name":"bytes-dns-linux-armv7","browser_download_url":"https://example.com/linux-armv7"},{"name":"checksums.txt","browser_download_url":"https://example.com/checksums.txt"}]}`))
	}))
	t.Cleanup(srv.Close)
	u := New(Options{RepoOwner: "bytes-commerce", RepoName: "bytes-dns", Version: "1.0.0", BaseURL: srv.URL, httpGet: defaultHTTPGet})
	rel, err := u.Latest(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rel.Tag != "1.2.3" {
		t.Errorf("Tag = %q, want %q", rel.Tag, "1.2.3")
	}
	if len(rel.Assets) != 4 {
		t.Errorf("len(Assets) = %d, want %d", len(rel.Assets), 4)
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
		_, _ = w.Write([]byte(`{"tag_name":"v2.0.0","assets":[]}`))
	}))
	t.Cleanup(srv.Close)
	u := New(Options{BaseURL: srv.URL, httpGet: defaultHTTPGet})
	rel, err := u.Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rel.Tag != "2.0.0" {
		t.Errorf("Tag = %q, want %q (v prefix should be stripped)", rel.Tag, "2.0.0")
	}
}

func TestAssetForPlatform(t *testing.T) {
	for _, tt := range []struct{ goos, goarch, goarm, want string }{{"linux", "amd64", "", "bytes-dns-linux-amd64"}, {"linux", "arm64", "", "bytes-dns-linux-arm64"}, {"linux", "arm", "7", "bytes-dns-linux-armv7"}} {
		t.Run(tt.want, func(t *testing.T) {
			if got := assetForPlatform(tt.goos, tt.goarch, tt.goarm); got != tt.want {
				t.Errorf("assetForPlatform(%q, %q, %q) = %q, want %q", tt.goos, tt.goarch, tt.goarm, got, tt.want)
			}
		})
	}
}
func TestAssetForPlatform_Unsupported(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("expected panic for unsupported platform darwin/amd64")
		}
	}()
	_ = assetForPlatform("darwin", "amd64", "")
}

func TestDownload_FetchesAsset(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("BINARY-CONTENTS")) }))
	t.Cleanup(srv.Close)
	u := New(Options{Version: "1.0.0", httpGet: defaultHTTPGet})
	got, err := u.Download(context.Background(), Asset{Name: "binary", URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "BINARY-CONTENTS" {
		t.Errorf("downloaded body = %q, want %q", got, "BINARY-CONTENTS")
	}
}

func TestDownload_AcceptsLargeBody(t *testing.T) {
	const bodySize = 6 << 20
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(make([]byte, bodySize)) }))
	t.Cleanup(srv.Close)
	u := New(Options{Version: "1.0.0", httpGet: defaultHTTPGet})
	got, err := u.Download(context.Background(), Asset{Name: "binary", URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != bodySize {
		t.Fatalf("downloaded body length = %d, want %d (server body length)", len(got), bodySize)
	}
}

func TestVerifyChecksum_Match(t *testing.T) {
	body := []byte("hello\n")
	checksums := []byte("5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03  bytes-dns-linux-amd64\n")
	if err := verifyChecksum(body, "bytes-dns-linux-amd64", checksums); err != nil {
		t.Errorf("expected match, got error: %v", err)
	}
}
func TestVerifyChecksum_Mismatch(t *testing.T) {
	err := verifyChecksum([]byte("hello\n"), "bytes-dns-linux-amd64", []byte("0000000000000000000000000000000000000000000000000000000000000000  bytes-dns-linux-amd64\n"))
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("unexpected error: %v", err)
	}
}
func TestVerifyChecksum_MissingEntry(t *testing.T) {
	err := verifyChecksum([]byte("hello\n"), "bytes-dns-linux-amd64", []byte("5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03  other-asset\n"))
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("unexpected error: %v", err)
	}
}
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
			return "bytes-dns development\n", nil
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
