package dns_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bytesbytes/bytes-dns/internal/dns"
)

type hetznerMock struct {
	zones   []map[string]any
	records []map[string]any
}

func (m *hetznerMock) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v1/zones", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			http.Error(w, `{"message":"unauthorized"}`, http.StatusUnauthorized)
			return
		}

		name := r.URL.Query().Get("name")
		var matched []map[string]any
		for _, z := range m.zones {
			if name == "" || z["name"] == name {
				matched = append(matched, z)
			}
		}

		resp := map[string]any{
			"zones": matched,
			"meta":  map[string]any{"pagination": map[string]any{"page": 1, "per_page": 100, "last_page": 1, "total_entries": len(matched)}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	mux.HandleFunc("/api/v1/records", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			http.Error(w, `{"message":"unauthorized"}`, http.StatusUnauthorized)
			return
		}

		switch r.Method {
		case http.MethodGet:
			zoneID := r.URL.Query().Get("zone_id")
			var matched []map[string]any
			for _, rec := range m.records {
				if zoneID == "" || rec["zone_id"] == zoneID {
					matched = append(matched, rec)
				}
			}
			resp := map[string]any{"records": matched}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)

		case http.MethodPost:
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			body["id"] = "new-record-id-001"
			resp := map[string]any{"record": body}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(resp)

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	mux.HandleFunc("/api/v1/records/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			http.Error(w, `{"message":"unauthorized"}`, http.StatusUnauthorized)
			return
		}

		if r.Method != http.MethodPut {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		recID := strings.TrimPrefix(r.URL.Path, "/api/v1/records/")
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		body["id"] = recID
		resp := map[string]any{"record": body}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	return mux
}

func newTestClient(t *testing.T, mock *hetznerMock) (*dns.Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(mock.handler())
	t.Cleanup(srv.Close)
	client := dns.NewWithBaseURL("test-token", srv.URL+"/api/v1")
	return client, srv
}

func TestFindZone_Found(t *testing.T) {
	mock := &hetznerMock{
		zones: []map[string]any{
			{"id": "zone-001", "name": "example.com"},
			{"id": "zone-002", "name": "other.org"},
		},
	}
	client, _ := newTestClient(t, mock)

	zone, err := client.FindZone(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if zone.ID != "zone-001" {
		t.Errorf("zone ID = %q, want %q", zone.ID, "zone-001")
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

func TestListRecords(t *testing.T) {
	mock := &hetznerMock{
		zones: []map[string]any{{"id": "zone-001", "name": "example.com"}},
		records: []map[string]any{
			{"id": "rec-001", "zone_id": "zone-001", "type": "A", "name": "home", "value": "1.2.3.4", "ttl": 60},
			{"id": "rec-002", "zone_id": "zone-001", "type": "A", "name": "mail", "value": "5.6.7.8", "ttl": 300},
		},
	}
	client, _ := newTestClient(t, mock)

	records, err := client.ListRecords(context.Background(), "zone-001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(records) != 2 {
		t.Errorf("got %d records, want 2", len(records))
	}
}

func TestFindRecord_Found(t *testing.T) {
	mock := &hetznerMock{
		records: []map[string]any{
			{"id": "rec-001", "zone_id": "zone-001", "type": "A", "name": "home", "value": "1.2.3.4", "ttl": 60},
		},
	}
	client, _ := newTestClient(t, mock)

	rec, err := client.FindRecord(context.Background(), "zone-001", "home", "A")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec == nil {
		t.Fatal("expected record, got nil")
	}
	if rec.ID != "rec-001" {
		t.Errorf("record ID = %q, want %q", rec.ID, "rec-001")
	}
	if rec.Value != "1.2.3.4" {
		t.Errorf("record Value = %q, want %q", rec.Value, "1.2.3.4")
	}
}

func TestFindRecord_NotFound(t *testing.T) {
	mock := &hetznerMock{records: []map[string]any{}}
	client, _ := newTestClient(t, mock)

	rec, err := client.FindRecord(context.Background(), "zone-001", "missing", "A")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec != nil {
		t.Errorf("expected nil record for missing name, got %+v", rec)
	}
}

func TestFindRecord_CaseInsensitive(t *testing.T) {
	mock := &hetznerMock{
		records: []map[string]any{
			{"id": "rec-001", "zone_id": "zone-001", "type": "A", "name": "Home", "value": "1.2.3.4", "ttl": 60},
		},
	}
	client, _ := newTestClient(t, mock)

	rec, err := client.FindRecord(context.Background(), "zone-001", "home", "A")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec == nil {
		t.Fatal("expected record (case-insensitive match), got nil")
	}
}

func TestUpdateRecord(t *testing.T) {
	mock := &hetznerMock{}
	client, _ := newTestClient(t, mock)

	existing := &dns.Record{
		ID:     "rec-001",
		ZoneID: "zone-001",
		Type:   "A",
		Name:   "home",
		Value:  "1.2.3.4",
		TTL:    60,
	}

	updated, err := client.UpdateRecord(context.Background(), existing, "9.9.9.9", 120)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated.Value != "9.9.9.9" {
		t.Errorf("updated value = %q, want %q", updated.Value, "9.9.9.9")
	}
	if updated.ID != "rec-001" {
		t.Errorf("record ID should be preserved, got %q", updated.ID)
	}
}

func TestCreateRecord(t *testing.T) {
	mock := &hetznerMock{}
	client, _ := newTestClient(t, mock)

	created, err := client.CreateRecord(context.Background(), "zone-001", "newhost", "A", "10.0.0.1", 60)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if created.ID != "new-record-id-001" {
		t.Errorf("created record ID = %q, want %q", created.ID, "new-record-id-001")
	}
	if created.Value != "10.0.0.1" {
		t.Errorf("created record value = %q, want %q", created.Value, "10.0.0.1")
	}
}
