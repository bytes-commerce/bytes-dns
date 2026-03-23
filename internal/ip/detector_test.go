package ip_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bytes-commerce/bytes-dns/internal/ip"
)

func serve(t *testing.T, body string, status int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

func TestDetectIPv4_Valid(t *testing.T) {
	srv := serve(t, "203.0.113.42\n", http.StatusOK)
	defer srv.Close()

	d := ip.New(srv.URL)
	got, err := d.DetectIPv4(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.String() != "203.0.113.42" {
		t.Errorf("got IP %q, want %q", got, "203.0.113.42")
	}
}

func TestDetectIPv4_WithWhitespace(t *testing.T) {
	srv := serve(t, "  1.2.3.4  ", http.StatusOK)
	defer srv.Close()

	d := ip.New(srv.URL)
	got, err := d.DetectIPv4(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.String() != "1.2.3.4" {
		t.Errorf("got IP %q, want %q", got, "1.2.3.4")
	}
}

func TestDetectIPv4_RejectsIPv6(t *testing.T) {
	srv := serve(t, "2001:db8::1", http.StatusOK)
	defer srv.Close()

	d := ip.New(srv.URL)
	_, err := d.DetectIPv4(context.Background())
	if err == nil {
		t.Fatal("expected error when IPv6 returned for A record, got nil")
	}
}

func TestDetectIPv4_NonIPResponse(t *testing.T) {
	srv := serve(t, "not an ip address", http.StatusOK)
	defer srv.Close()

	d := ip.New(srv.URL)
	_, err := d.DetectIPv4(context.Background())
	if err == nil {
		t.Fatal("expected error for non-IP response, got nil")
	}
}

func TestDetectIPv4_HTTP500(t *testing.T) {
	srv := serve(t, "internal server error", http.StatusInternalServerError)
	defer srv.Close()

	d := ip.New(srv.URL)
	_, err := d.DetectIPv4(context.Background())
	if err == nil {
		t.Fatal("expected error for HTTP 500, got nil")
	}
}

func TestDetectIPv4_EmptyBody(t *testing.T) {
	srv := serve(t, "", http.StatusOK)
	defer srv.Close()

	d := ip.New(srv.URL)
	_, err := d.DetectIPv4(context.Background())
	if err == nil {
		t.Fatal("expected error for empty response body, got nil")
	}
}

func TestDetectIPv4_ServerDown(t *testing.T) {
	d := ip.New("http://127.0.0.1:1")
	_, err := d.DetectIPv4(context.Background())
	if err == nil {
		t.Fatal("expected error for unreachable server, got nil")
	}
}

func TestDetectIPv6_Valid(t *testing.T) {
	srv := serve(t, "2001:db8::42", http.StatusOK)
	defer srv.Close()

	d := ip.New(srv.URL)
	got, err := d.DetectIPv6(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.String() != "2001:db8::42" {
		t.Errorf("got IP %q, want %q", got, "2001:db8::42")
	}
}

func TestDetectIPv6_RejectsIPv4(t *testing.T) {
	srv := serve(t, "1.2.3.4", http.StatusOK)
	defer srv.Close()

	d := ip.New(srv.URL)
	_, err := d.DetectIPv6(context.Background())
	if err == nil {
		t.Fatal("expected error when IPv4 returned for AAAA record, got nil")
	}
}

func TestDetectIPv4_CancelledContext(t *testing.T) {
	srv := serve(t, "1.2.3.4", http.StatusOK)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	d := ip.New(srv.URL)
	_, err := d.DetectIPv4(ctx)
	if err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}
}
