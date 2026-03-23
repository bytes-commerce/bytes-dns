package updater_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bytesbytes/bytes-dns/internal/config"
	"github.com/bytesbytes/bytes-dns/internal/dns"
	"github.com/bytesbytes/bytes-dns/internal/state"
	"github.com/bytesbytes/bytes-dns/internal/updater"
)

func hetznerAPIHandler(existingRecord *dns.Record) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/zones"):
			json.NewEncoder(w).Encode(map[string]any{
				"zones": []map[string]any{{"id": "zone-001", "name": "example.com"}},
				"meta":  map[string]any{"pagination": map[string]any{}},
			})

		case r.Method == http.MethodGet && r.URL.Path == "/records":
			records := []map[string]any{}
			if existingRecord != nil {
				records = append(records, map[string]any{
					"id":      existingRecord.ID,
					"zone_id": existingRecord.ZoneID,
					"type":    existingRecord.Type,
					"name":    existingRecord.Name,
					"value":   existingRecord.Value,
					"ttl":     existingRecord.TTL,
				})
			}
			json.NewEncoder(w).Encode(map[string]any{"records": records})

		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/records/"):
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			recID := strings.TrimPrefix(r.URL.Path, "/records/")
			body["id"] = recID
			json.NewEncoder(w).Encode(map[string]any{"record": body})

		case r.Method == http.MethodPost && r.URL.Path == "/records":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			body["id"] = "created-record-001"
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{"record": body})

		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	})
}

func setupServers(t *testing.T, publicIP string, existingRecord *dns.Record) (*updater.Updater, *state.Manager) {
	t.Helper()

	ipSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(publicIP))
	}))
	t.Cleanup(ipSrv.Close)

	apiSrv := httptest.NewServer(hetznerAPIHandler(existingRecord))
	t.Cleanup(apiSrv.Close)

	cfg := &config.Config{
		APIToken:        "test-token",
		Zone:            "example.com",
		Record:          "home.example.com",
		RecordType:      "A",
		TTL:             60,
		IntervalMinutes: 5,
		IPSource:        ipSrv.URL,
		LogLevel:        "error",
		AllowPrivateIP:  true,
		DryRun:          false,
	}

	dir := t.TempDir()
	sm := state.New(filepath.Join(dir, "state.json"))
	u := updater.NewWithDNSClient(cfg, sm, dns.NewWithBaseURL("test-token", apiSrv.URL))
	return u, sm
}

func TestRun_CreateNewRecord(t *testing.T) {
	u, _ := setupServers(t, "5.6.7.8", nil)

	result, err := u.Run(context.Background(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Action != updater.ActionCreated {
		t.Errorf("action = %q, want %q", result.Action, updater.ActionCreated)
	}
	if result.PublicIP != "5.6.7.8" {
		t.Errorf("PublicIP = %q, want %q", result.PublicIP, "5.6.7.8")
	}
}

func TestRun_UpdateExistingRecord(t *testing.T) {
	existing := &dns.Record{
		ID: "rec-001", ZoneID: "zone-001", Type: "A", Name: "home", Value: "1.2.3.4", TTL: 60,
	}
	u, _ := setupServers(t, "9.9.9.9", existing)

	result, err := u.Run(context.Background(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Action != updater.ActionUpdated {
		t.Errorf("action = %q, want %q", result.Action, updater.ActionUpdated)
	}
	if result.PublicIP != "9.9.9.9" {
		t.Errorf("PublicIP = %q, want %q", result.PublicIP, "9.9.9.9")
	}
}

func TestRun_NoChangeWhenIPMatches(t *testing.T) {
	existing := &dns.Record{
		ID: "rec-001", ZoneID: "zone-001", Type: "A", Name: "home", Value: "1.2.3.4", TTL: 60,
	}
	u, _ := setupServers(t, "1.2.3.4", existing)

	result, err := u.Run(context.Background(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Action != updater.ActionNoChange {
		t.Errorf("action = %q, want %q", result.Action, updater.ActionNoChange)
	}
}

func TestRun_SkipsAPIWhenCacheMatches(t *testing.T) {
	existing := &dns.Record{
		ID: "rec-001", ZoneID: "zone-001", Type: "A", Name: "home", Value: "1.2.3.4", TTL: 60,
	}
	u, sm := setupServers(t, "1.2.3.4", existing)

	st, _ := sm.Load()
	_ = sm.MarkUpdated(st, "1.2.3.4", "rec-001")

	result, err := u.Run(context.Background(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Action != updater.ActionNoChange {
		t.Errorf("action = %q, want %q (cached match should skip API)", result.Action, updater.ActionNoChange)
	}
}

func TestRun_ForceBypassesCache(t *testing.T) {
	existing := &dns.Record{
		ID: "rec-001", ZoneID: "zone-001", Type: "A", Name: "home", Value: "1.2.3.4", TTL: 60,
	}
	u, sm := setupServers(t, "1.2.3.4", existing)

	st, _ := sm.Load()
	_ = sm.MarkUpdated(st, "1.2.3.4", "rec-001")

	result, err := u.Run(context.Background(), true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Action != updater.ActionNoChange {
		t.Errorf("action = %q, want %q (live record matches, no write needed)", result.Action, updater.ActionNoChange)
	}
}

func TestRun_DryRunCreate(t *testing.T) {
	ipSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("5.6.7.8"))
	}))
	t.Cleanup(ipSrv.Close)

	apiSrv := httptest.NewServer(hetznerAPIHandler(nil))
	t.Cleanup(apiSrv.Close)

	cfg := &config.Config{
		APIToken: "test-token", Zone: "example.com", Record: "home.example.com",
		RecordType: "A", TTL: 60, IPSource: ipSrv.URL, LogLevel: "error",
		AllowPrivateIP: true, DryRun: true,
	}

	dir := t.TempDir()
	sm := state.New(filepath.Join(dir, "state.json"))
	dryUpdater := updater.NewWithDNSClient(cfg, sm, dns.NewWithBaseURL("test-token", apiSrv.URL))

	result, err := dryUpdater.Run(context.Background(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.DryRun {
		t.Error("expected DryRun=true in result")
	}
	if result.Action != updater.ActionCreated {
		t.Errorf("action = %q, want %q", result.Action, updater.ActionCreated)
	}
}
