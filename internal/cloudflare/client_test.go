package cloudflare_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/bytes-commerce/bytes-dns/internal/cloudflare"
	"github.com/bytes-commerce/bytes-dns/internal/dns"
)

const testZoneID = "023e105f4ecef8ad9ca31a8372d0c353"

// capturedRequest records what the mock API received.
type capturedRequest struct {
	method string
	path   string
	query  string
	body   map[string]any
}

// cfMock is a minimal stand-in for the Cloudflare DNS API.
type cfMock struct {
	mu sync.Mutex

	zoneName string
	records  []map[string]any

	// failWithStatus makes every request fail with that HTTP status.
	failWithStatus int
	// envelopeError makes the API answer with HTTP 200 but success:false.
	envelopeError string

	requests []capturedRequest
}

func (m *cfMock) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/zones", func(w http.ResponseWriter, r *http.Request) {
		m.record(r, nil)

		if m.failWithStatus != 0 {
			w.WriteHeader(m.failWithStatus)
			_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":1000,"message":"nope"}]}`))
			return
		}
		if m.envelopeError != "" {
			m.writeEnvelopeError(w)
			return
		}

		name := r.URL.Query().Get("name")
		var result []map[string]any
		if name == "" || strings.EqualFold(name, m.zoneName) {
			result = append(result, map[string]any{
				"id":     testZoneID,
				"name":   m.zoneName,
				"status": "active",
			})
		}

		m.writeResult(w, result)
	})

	mux.HandleFunc("/zones/", func(w http.ResponseWriter, r *http.Request) {
		body := m.readBody(r)
		m.record(r, body)

		if m.failWithStatus != 0 {
			w.WriteHeader(m.failWithStatus)
			_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":1000,"message":"nope"}]}`))
			return
		}
		if m.envelopeError != "" {
			m.writeEnvelopeError(w)
			return
		}

		switch r.Method {
		case http.MethodGet:
			name := r.URL.Query().Get("name")
			var result []map[string]any
			for _, rec := range m.records {
				if name == "" || strings.EqualFold(rec["name"].(string), name) {
					result = append(result, rec)
				}
			}
			m.writeResult(w, result)
		case http.MethodPost:
			created := map[string]any{
				"id":      "created-record-id",
				"name":    body["name"],
				"type":    body["type"],
				"content": body["content"],
				"ttl":     body["ttl"],
				"proxied": body["proxied"],
				"comment": body["comment"],
			}
			m.writeResult(w, created)
		case http.MethodPatch:
			updated := map[string]any{
				"id":      "existing-record-id",
				"name":    "home.example.com",
				"type":    "A",
				"content": body["content"],
				"ttl":     60,
				"proxied": true,
			}
			m.writeResult(w, updated)
		default:
			http.Error(w, "unsupported method", http.StatusMethodNotAllowed)
		}
	})

	return mux
}

func (m *cfMock) writeResult(w http.ResponseWriter, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success":  true,
		"errors":   []any{},
		"messages": []any{},
		"result":   result,
	})
}

func (m *cfMock) writeEnvelopeError(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success": false,
		"errors": []map[string]any{
			{"code": 1004, "message": m.envelopeError},
		},
		"messages": []any{},
		"result":   nil,
	})
}

func (m *cfMock) readBody(r *http.Request) map[string]any {
	if r.Body == nil {
		return nil
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return nil
	}
	return body
}

func (m *cfMock) record(r *http.Request, body map[string]any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, capturedRequest{
		method: r.Method,
		path:   r.URL.Path,
		query:  r.URL.RawQuery,
		body:   body,
	})
}

func (m *cfMock) lastRequest(t *testing.T) capturedRequest {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.requests) == 0 {
		t.Fatal("no request was recorded")
	}
	return m.requests[len(m.requests)-1]
}

func newTestClient(t *testing.T, mock *cfMock) *cloudflare.Client {
	t.Helper()
	srv := httptest.NewServer(mock.handler())
	t.Cleanup(srv.Close)

	return cloudflare.New(cloudflare.Options{
		APIToken: "test-token",
		Zone:     mock.zoneName,
		BaseURL:  srv.URL,
	})
}

func testZone() *dns.Zone {
	return &dns.Zone{ID: dns.ZoneID(testZoneID), Name: "example.com"}
}

func TestFindZone_Found(t *testing.T) {
	mock := &cfMock{zoneName: "example.com"}
	client := newTestClient(t, mock)

	zone, err := client.FindZone(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(zone.ID) != testZoneID {
		t.Errorf("zone ID = %q, want %q", zone.ID, testZoneID)
	}
	if zone.Name != "example.com" {
		t.Errorf("zone name = %q, want %q", zone.Name, "example.com")
	}
}

func TestFindZone_NotFound(t *testing.T) {
	mock := &cfMock{zoneName: "other.com"}
	client := newTestClient(t, mock)

	_, err := client.FindZone(context.Background(), "example.com")
	if err == nil {
		t.Fatal("expected error for missing zone, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected a not found error, got: %v", err)
	}
}

func TestFindZoneByRecord(t *testing.T) {
	mock := &cfMock{zoneName: "example.com"}
	client := newTestClient(t, mock)

	zone, err := client.FindZoneByRecord(context.Background(), "home.example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(zone.ID) != testZoneID {
		t.Errorf("zone ID = %q, want %q", zone.ID, testZoneID)
	}
}

func TestFindZoneByRecord_OutsideZone(t *testing.T) {
	mock := &cfMock{zoneName: "example.com"}
	client := newTestClient(t, mock)

	_, err := client.FindZoneByRecord(context.Background(), "home.other.com")
	if err == nil {
		t.Fatal("expected error for record outside the configured zone, got nil")
	}
}

func TestFindRRSet_Found(t *testing.T) {
	mock := &cfMock{
		zoneName: "example.com",
		records: []map[string]any{
			{
				"id":      "existing-record-id",
				"name":    "home.example.com",
				"type":    "A",
				"content": "1.2.3.4",
				"ttl":     60,
				"proxied": true,
			},
		},
	}
	client := newTestClient(t, mock)

	rrset, err := client.FindRRSet(context.Background(), testZone(), "home.example.com", "A")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rrset == nil {
		t.Fatal("expected RRSet, got nil")
	}
	if rrset.ID != "existing-record-id" {
		t.Errorf("rrset ID = %q, want %q", rrset.ID, "existing-record-id")
	}
	if len(rrset.Records) != 1 || rrset.Records[0].Value != "1.2.3.4" {
		t.Errorf("rrset records = %+v, want [{1.2.3.4}]", rrset.Records)
	}
	if rrset.Zone != dns.ZoneID(testZoneID) {
		t.Errorf("rrset zone = %q, want %q", rrset.Zone, testZoneID)
	}
	if rrset.Proxied == nil {
		t.Fatal("expected the proxied flag to be reported")
	}
	if !*rrset.Proxied {
		t.Error("expected the proxied flag to be true")
	}
}

func TestFindRRSet_NotFound(t *testing.T) {
	mock := &cfMock{zoneName: "example.com"}
	client := newTestClient(t, mock)

	rrset, err := client.FindRRSet(context.Background(), testZone(), "home.example.com", "A")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rrset != nil {
		t.Errorf("expected nil RRSet, got %+v", rrset)
	}
}

func TestCreateRRSet_ProxiedByDefault(t *testing.T) {
	mock := &cfMock{zoneName: "example.com"}
	client := newTestClient(t, mock)

	created, err := client.CreateRRSet(context.Background(), testZone(), "home.example.com", "A", "5.6.7.8", 60, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(created.Records) != 1 || created.Records[0].Value != "5.6.7.8" {
		t.Errorf("created records = %+v, want [{5.6.7.8}]", created.Records)
	}

	req := mock.lastRequest(t)
	if req.method != http.MethodPost {
		t.Errorf("method = %q, want POST", req.method)
	}
	if proxied, ok := req.body["proxied"].(bool); !ok || !proxied {
		t.Errorf("proxied = %v, want true", req.body["proxied"])
	}
	if req.body["content"] != "5.6.7.8" {
		t.Errorf("content = %v, want 5.6.7.8", req.body["content"])
	}
	if req.body["ttl"] != float64(60) {
		t.Errorf("ttl = %v, want 60", req.body["ttl"])
	}
}

func TestCreateRRSet_ProxiedDisabled(t *testing.T) {
	mock := &cfMock{zoneName: "example.com"}
	client := newTestClient(t, mock)

	proxied := false
	_, err := client.CreateRRSet(context.Background(), testZone(), "home.example.com", "A", "5.6.7.8", 60, &dns.CreateOptions{Proxied: &proxied})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	req := mock.lastRequest(t)
	if proxiedValue, ok := req.body["proxied"].(bool); !ok || proxiedValue {
		t.Errorf("proxied = %v, want false", req.body["proxied"])
	}
}

func TestUpdateRRSet_DoesNotTouchProxied(t *testing.T) {
	mock := &cfMock{zoneName: "example.com"}
	client := newTestClient(t, mock)

	rrset := &dns.RRSet{
		ID:      "existing-record-id",
		Name:    "home.example.com",
		Type:    "A",
		TTL:     60,
		Records: []dns.RecordValue{{Value: "1.2.3.4"}},
	}

	updated, err := client.UpdateRRSet(context.Background(), testZone(), rrset, "9.9.9.9")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(updated.Records) != 1 || updated.Records[0].Value != "9.9.9.9" {
		t.Errorf("updated records = %+v, want [{9.9.9.9}]", updated.Records)
	}

	req := mock.lastRequest(t)
	if req.method != http.MethodPatch {
		t.Errorf("method = %q, want PATCH", req.method)
	}
	if _, ok := req.body["proxied"]; ok {
		t.Errorf("expected the proxied flag to be untouched, got body %+v", req.body)
	}
	if len(req.body) != 1 || req.body["content"] != "9.9.9.9" {
		t.Errorf("expected only the content to be patched, got body %+v", req.body)
	}
}

func TestEnvelopeErrorIsReported(t *testing.T) {
	mock := &cfMock{zoneName: "example.com", envelopeError: "Invalid DNS record content"}
	client := newTestClient(t, mock)

	_, err := client.CreateRRSet(context.Background(), testZone(), "home.example.com", "A", "not-an-ip", 60, nil)
	if err == nil {
		t.Fatal("expected error for a failed envelope, got nil")
	}
	if !strings.Contains(err.Error(), "Invalid DNS record content") {
		t.Errorf("expected the Cloudflare error message, got: %v", err)
	}
}

func TestUnauthorized(t *testing.T) {
	mock := &cfMock{zoneName: "example.com", failWithStatus: http.StatusUnauthorized}
	client := newTestClient(t, mock)

	_, err := client.FindZone(context.Background(), "example.com")
	if err == nil || !strings.Contains(err.Error(), "authentication failed") {
		t.Fatalf("expected an authentication error, got: %v", err)
	}
}

func TestForbidden(t *testing.T) {
	mock := &cfMock{zoneName: "example.com", failWithStatus: http.StatusForbidden}
	client := newTestClient(t, mock)

	_, err := client.FindZone(context.Background(), "example.com")
	if err == nil || !strings.Contains(err.Error(), "access denied") {
		t.Fatalf("expected an access denied error, got: %v", err)
	}
}

func TestUsesCachedZoneID(t *testing.T) {
	mock := &cfMock{zoneName: "example.com"}
	srv := httptest.NewServer(mock.handler())
	t.Cleanup(srv.Close)

	client := cloudflare.New(cloudflare.Options{
		APIToken: "test-token",
		Zone:     "example.com",
		ZoneID:   testZoneID,
		BaseURL:  srv.URL,
	})

	zone, err := client.FindZone(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(zone.ID) != testZoneID {
		t.Errorf("zone ID = %q, want %q", zone.ID, testZoneID)
	}

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.requests) != 0 {
		t.Errorf("expected no API call for a cached zone id, got %d requests", len(mock.requests))
	}
}
