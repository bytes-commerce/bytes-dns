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
			t.Errorf("expected Accept header")
		}
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "bytes-dns/") {
			t.Errorf("expected User-Agent")
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
		t.Errorf("Tag = %q", rel.Tag)
	}
	if len(rel.Assets) != 4 {
		t.Errorf("len(Assets) = %d", len(rel.Assets))
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
		t.Errorf("Tag = %q", rel.Tag)
	}
}

func TestAssetForPlatform(t *testing.T) {
	for _, tt := range []struct{ goos, goarch, goarm, want string }{{"linux", "amd64", "", "bytes-dns-linux-amd64"}, {"linux", "arm64", "", "bytes-dns-linux-arm64"}, {"linux", "arm", "7", "bytes-dns-linux-armv7"}} {
		t.Run(tt.want, func(t *testing.T) {
			if got := assetForPlatform(tt.goos, tt.goarch, tt.goarm); got != tt.want {
				t.Errorf("got %q", got)
			}
		})
	}
}
func TestAssetForPlatform_Unsupported(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("expected panic")
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
		t.Errorf("downloaded body = %q", got)
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
		t.Error(err)
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
