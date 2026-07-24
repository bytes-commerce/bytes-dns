package selfupdate

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
