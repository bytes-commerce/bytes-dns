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
