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
			for _, rr := range m.rrsets {
				matched = append(matched, rr)
			}
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
