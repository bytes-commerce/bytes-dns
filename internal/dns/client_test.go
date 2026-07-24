package dns_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bytes-commerce/bytes-dns/internal/dns"
)

type hetznerMock struct {
	zones  []map[string]any
	rrsets []map[string]any
}

func (m *hetznerMock) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/v1/zones", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			http.Error(w, `{"message":"unauthorized"}`, http.StatusUnauthorized)
			return
		}

		if r.Method == http.MethodPost {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			body["id"] = 999
			resp := map[string]any{"zone": body}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		name := r.URL.Query().Get("name")
		var matched []map[string]any
		matched = append(matched, m.zones...)
		if name != "" {
			var filtered []map[string]any
			for _, z := range m.zones {
				if strings.EqualFold(z["name"].(string), name) {
					filtered = append(filtered, z)
				}
			}
			matched = filtered
		}

		resp := map[string]any{
			"zones": matched,
			"meta":  map[string]any{"pagination": map[string]any{"page": 1, "per_page": 100, "last_page": 1, "total_entries": len(matched)}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	mux.HandleFunc("/v1/zones/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			http.Error(w, `{"message":"unauthorized"}`, http.StatusUnauthorized)
			return
		}

		path := r.URL.Path
		if strings.HasSuffix(path, "/rrsets") {
			// POST /v1/zones/{id}/rrsets
			if r.Method == http.MethodPost {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					http.Error(w, "bad request", http.StatusBadRequest)
					return
				}
				body["id"] = body["name"].(string) + "/" + body["type"].(string)
				resp := map[string]any{"rrset": body}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(resp)
				return
			}
			// GET /v1/zones/{id}/rrsets
			var matched []map[string]any
			matched = append(matched, m.rrsets...)
			resp := map[string]any{"rrsets": matched, "meta": map[string]any{"pagination": map[string]any{}}}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		// PUT /v1/zones/{id}/rrsets/{name}/{type}
		if r.Method == http.MethodPut && strings.Contains(path, "/rrsets/") {
			w.WriteHeader(http.StatusOK)
			return
		}

		http.Error(w, "not found", http.StatusNotFound)
	})

	return mux
}

func newTestClient(t *testing.T, mock *hetznerMock) (*dns.Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(mock.handler())
	t.Cleanup(srv.Close)
	client := dns.NewWithBaseURL("test-token", srv.URL+"/v1")
	return client, srv
}

func TestFindZone_Found(t *testing.T) {
	mock := &hetznerMock{
		zones: []map[string]any{
			{"id": 42, "name": "example.com"},
			{"id": 43, "name": "other.org"},
		},
	}
	client, _ := newTestClient(t, mock)

	zone, err := client.FindZone(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if zone.ID != 42 {
		t.Errorf("zone ID = %d, want %d", zone.ID, 42)
	}
	if zone.Name != "example.com" {
		t.Errorf("zone Name = %q, want %q", zone.Name, "example.com")
	}
}

func TestFindZone_NotFound(t *testing.T) {
	mock := &hetznerMock{zones: []map[string]any{}}
	client, _ := newTestClient(t, mock)

	_, err := client.FindZone(context.Background(), "nonexistent.com")
	if err == nil {
		t.Fatal("expected error for missing zone, got nil")
	}
}

func TestListRRSets(t *testing.T) {
	mock := &hetznerMock{
		rrsets: []map[string]any{
			{"id": "home/A", "name": "home", "type": "A", "ttl": 60, "records": []map[string]any{{"value": "1.2.3.4"}}},
			{"id": "mail/A", "name": "mail", "type": "A", "ttl": 300, "records": []map[string]any{{"value": "5.6.7.8"}}},
		},
	}
	client, _ := newTestClient(t, mock)

	rrsets, err := client.ListRRSets(context.Background(), "42", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rrsets) != 2 {
		t.Errorf("got %d rrsets, want 2", len(rrsets))
	}
}

func TestFindRRSet_Found(t *testing.T) {
	mock := &hetznerMock{
		rrsets: []map[string]any{
			{"id": "home/A", "name": "home", "type": "A", "ttl": 60, "records": []map[string]any{{"value": "1.2.3.4"}}},
		},
	}
	client, _ := newTestClient(t, mock)

	rr, err := client.FindRRSet(context.Background(), "42", "home", "A")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rr == nil {
		t.Fatal("expected RRSet, got nil")
	}
	if rr.Name != "home" {
		t.Errorf("RRSet Name = %q, want %q", rr.Name, "home")
	}
	if rr.Records[0].Value != "1.2.3.4" {
		t.Errorf("RRSet Value = %q, want %q", rr.Records[0].Value, "1.2.3.4")
	}
}

func TestUpdateRRSet(t *testing.T) {
	mock := &hetznerMock{}
	client, _ := newTestClient(t, mock)

	existing := &dns.RRSet{
		ID:   "home/A",
		Name: "home",
		Type: "A",
		TTL:  60,
		Records: []dns.RecordValue{
			{Value: "1.2.3.4"},
		},
	}

	updated, err := client.UpdateRRSet(context.Background(), "42", existing, "9.9.9.9")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated.Records[0].Value != "9.9.9.9" {
		t.Errorf("updated value = %q, want %q", updated.Records[0].Value, "9.9.9.9")
	}
}

func TestCreateRRSet(t *testing.T) {
	mock := &hetznerMock{}
	client, _ := newTestClient(t, mock)

	created, err := client.CreateRRSet(context.Background(), "42", "newhost", "A", "10.0.0.1", 60)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if created.ID != "newhost/A" {
		t.Errorf("created RRSet ID = %q, want %q", created.ID, "newhost/A")
	}
	if created.Records[0].Value != "10.0.0.1" {
		t.Errorf("created record value = %q, want %q", created.Records[0].Value, "10.0.0.1")
	}
}

func TestFindZoneByRecord(t *testing.T) {
	mock := &hetznerMock{
		zones: []map[string]any{
			{"id": 42, "name": "example.com"},
			{"id": 43, "name": "sub.example.com"},
			{"id": 44, "name": "other.org"},
		},
	}
	client, _ := newTestClient(t, mock)

	tests := []struct {
		record string
		wantID int
	}{
		{"home.example.com", 42},
		{"home.sub.example.com", 43},
		{"example.com", 42},
		{"sub.example.com", 43},
		{"other.org", 44},
		{"www.other.org", 44},
	}

	for _, tt := range tests {
		t.Run(tt.record, func(t *testing.T) {
			zone, err := client.FindZoneByRecord(context.Background(), tt.record)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if zone.ID != tt.wantID {
				t.Errorf("zone ID = %d, want %d", zone.ID, tt.wantID)
			}
		})
	}
}

func TestCreateZone(t *testing.T) {
	mock := &hetznerMock{}
	client, _ := newTestClient(t, mock)

	zone, err := client.CreateZone(context.Background(), "newzone.com", 3600)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if zone.Name != "newzone.com" {
		t.Errorf("zone Name = %q, want %q", zone.Name, "newzone.com")
	}
	if zone.ID != 999 {
		t.Errorf("zone ID = %d, want %d", zone.ID, 999)
	}
}

func TestNew(t *testing.T) {
	c := dns.New("test-token")
	if c == nil {
		t.Fatal("expected client, got nil")
	}
}

func TestClient_AuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"unauthorized"}`))
	}))
	defer srv.Close()

	client := dns.NewWithBaseURL("wrong-token", srv.URL)
	_, err := client.FindZone(context.Background(), "example.com")
	if err == nil || !strings.Contains(err.Error(), "authentication failed") {
		t.Errorf("expected auth error, got %v", err)
	}
}

func TestClient_ForbiddenError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"forbidden"}`))
	}))
	defer srv.Close()

	client := dns.NewWithBaseURL("token", srv.URL)
	_, err := client.FindZone(context.Background(), "example.com")
	if err == nil || !strings.Contains(err.Error(), "access denied") {
		t.Errorf("expected forbidden error, got %v", err)
	}
}

func TestClient_RetriesOn5xx(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"zones":[{"id":42,"name":"example.com"}],"meta":{}}`))
	}))
	defer srv.Close()

	client := dns.NewWithBaseURL("token", srv.URL)
	zone, err := client.FindZone(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("unexpected error after retries: %v", err)
	}
	if zone.ID != 42 {
		t.Errorf("zone ID = %d, want 42", zone.ID)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3 (succeeded on 3rd attempt)", calls)
	}
}

func TestClient_DoesNotRetryOn401(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	client := dns.NewWithBaseURL("wrong", srv.URL)
	_, err := client.FindZone(context.Background(), "example.com")
	if err == nil {
		t.Fatal("expected 401 error, got nil")
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (no retry on 401)", calls)
	}
}

func TestClient_RetriesOn429(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"zones":[{"id":42,"name":"anything"}],"meta":{}}`))
	}))
	defer srv.Close()

	client := dns.NewWithBaseURL("token", srv.URL)
	_, err := client.FindZone(context.Background(), "anything")
	if err != nil {
		t.Fatalf("unexpected error after retry on 429: %v", err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2 (retried once on 429)", calls)
	}
}

func TestClient_GivesUpAfter3Attempts(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := dns.NewWithBaseURL("token", srv.URL)
	_, err := client.FindZone(context.Background(), "anything")
	if err == nil {
		t.Fatal("expected error after exhausting retries, got nil")
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3 (final attempt)", calls)
	}
}

func TestUpdateRRSet_DoesNotMutateInput(t *testing.T) {
	mock := &hetznerMock{}
	client, _ := newTestClient(t, mock)

	original := &dns.RRSet{
		ID:   "home/A",
		Name: "home",
		Type: "A",
		TTL:  60,
		Records: []dns.RecordValue{
			{Value: "1.2.3.4"},
		},
	}

	// Snapshot the original.
	beforeRecords := make([]dns.RecordValue, len(original.Records))
	copy(beforeRecords, original.Records)

	_, err := client.UpdateRRSet(context.Background(), "42", original, "9.9.9.9")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(original.Records) != len(beforeRecords) {
		t.Fatalf("original.Records length changed: was %d, now %d",
			len(beforeRecords), len(original.Records))
	}
	for i := range beforeRecords {
		if original.Records[i].Value != beforeRecords[i].Value {
			t.Errorf("original.Records[%d].Value = %q, want %q (input was mutated)",
				i, original.Records[i].Value, beforeRecords[i].Value)
		}
	}
}
