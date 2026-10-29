package updater_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/bytes-commerce/bytes-dns/internal/cloudflare"
	"github.com/bytes-commerce/bytes-dns/internal/config"
	"github.com/bytes-commerce/bytes-dns/internal/state"
	"github.com/bytes-commerce/bytes-dns/internal/updater"
)

const cfZoneID = "023e105f4ecef8ad9ca31a8372d0c353"

// cfAPI is a minimal stand-in for the Cloudflare DNS API.
type cfAPI struct {
	mu sync.Mutex

	records []map[string]any
	// failWith makes every write fail with this HTTP status.
	failWith int

	created []map[string]any
	patched []map[string]any
	writes  int
}

func (a *cfAPI) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/zones", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		result := []map[string]any{}
		if name == "" || strings.EqualFold(name, "example.com") {
			result = append(result, map[string]any{"id": cfZoneID, "name": "example.com", "status": "active"})
		}
		writeCFResult(w, result)
	})

	mux.HandleFunc("/zones/", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}

		a.mu.Lock()
		defer a.mu.Unlock()

		if a.failWith != 0 {
			w.WriteHeader(a.failWith)
			_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":1000,"message":"boom"}]}`))
			return
		}

		switch r.Method {
		case http.MethodGet:
			name := r.URL.Query().Get("name")
			result := []map[string]any{}
			for _, rec := range a.records {
				if name == "" || strings.EqualFold(rec["name"].(string), name) {
					result = append(result, rec)
				}
			}
			writeCFResult(w, result)
		case http.MethodPost:
			a.writes++
			a.created = append(a.created, body)
			writeCFResult(w, map[string]any{
				"id":      "new-record-id",
				"name":    body["name"],
				"type":    body["type"],
				"content": body["content"],
				"ttl":     body["ttl"],
				"proxied": body["proxied"],
			})
		case http.MethodPatch:
			a.writes++
			a.patched = append(a.patched, body)
			writeCFResult(w, map[string]any{
				"id":      "existing-record-id",
				"name":    "home.example.com",
				"type":    "A",
				"content": body["content"],
				"ttl":     60,
				"proxied": true,
			})
		default:
			http.Error(w, "unsupported method", http.StatusMethodNotAllowed)
		}
	})

	return mux
}

func writeCFResult(w http.ResponseWriter, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"errors":  []any{},
		"result":  result,
	})
}

// setupCloudflare wires an updater for the Cloudflare provider against a
// mock API.
func setupCloudflare(t *testing.T, publicIP string, api *cfAPI, proxied *bool) (*updater.Updater, *state.Manager) {
	return setupCloudflareWithOptions(t, publicIP, api, proxied, false)
}

func setupCloudflareWithOptions(t *testing.T, publicIP string, api *cfAPI, proxied *bool, dryRun bool) (*updater.Updater, *state.Manager) {
	t.Helper()

	// Keep a possible zone write-back inside the test directory.
	t.Setenv("HOME", t.TempDir())

	ipSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(publicIP))
	}))
	t.Cleanup(ipSrv.Close)

	apiSrv := httptest.NewServer(api.handler())
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
		DryRun:          dryRun,
		Cloudflare: &config.CloudflareConfig{
			APIToken: "cf-token",
			Proxied:  proxied,
		},
	}

	provider := config.ProviderConfig{
		Key:        config.ProviderCloudflare,
		APIToken:   "cf-token",
		Zone:       "example.com",
		Record:     "home.example.com",
		RecordType: "A",
		TTL:        60,
		Proxied:    proxied,
	}

	client := cloudflare.New(cloudflare.Options{
		APIToken: "cf-token",
		Zone:     "example.com",
		BaseURL:  apiSrv.URL,
	})

	sm := state.New(filepath.Join(t.TempDir(), "state.json"))
	u := updater.NewForProvider(updater.Options{
		Config:   cfg,
		State:    sm,
		Provider: provider,
		Client:   client,
	})

	return u, sm
}

func TestRun_CloudflareCreatesRecord(t *testing.T) {
	api := &cfAPI{}
	u, _ := setupCloudflare(t, "5.6.7.8", api, nil)

	result, err := u.Run(context.Background(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != updater.ActionCreated {
		t.Errorf("action = %q, want %q", result.Action, updater.ActionCreated)
	}
	if result.Provider != config.ProviderCloudflare {
		t.Errorf("provider = %q, want %q", result.Provider, config.ProviderCloudflare)
	}

	if len(api.created) != 1 {
		t.Fatalf("got %d create requests, want 1", len(api.created))
	}
	if proxied, ok := api.created[0]["proxied"].(bool); !ok || !proxied {
		t.Errorf("proxied = %v, want true", api.created[0]["proxied"])
	}
	if api.created[0]["content"] != "5.6.7.8" {
		t.Errorf("content = %v, want 5.6.7.8", api.created[0]["content"])
	}
}

func TestRun_CloudflareCreatesRecordWithoutProxy(t *testing.T) {
	proxied := false
	api := &cfAPI{}
	u, _ := setupCloudflare(t, "5.6.7.8", api, &proxied)

	if _, err := u.Run(context.Background(), false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(api.created) != 1 {
		t.Fatalf("got %d create requests, want 1", len(api.created))
	}
	if value, ok := api.created[0]["proxied"].(bool); !ok || value {
		t.Errorf("proxied = %v, want false", api.created[0]["proxied"])
	}
}

func TestRun_CloudflareUpdatesExistingRecord(t *testing.T) {
	api := &cfAPI{
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
	u, _ := setupCloudflare(t, "9.9.9.9", api, nil)

	result, err := u.Run(context.Background(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != updater.ActionUpdated {
		t.Errorf("action = %q, want %q", result.Action, updater.ActionUpdated)
	}

	if len(api.patched) != 1 {
		t.Fatalf("got %d patch requests, want 1", len(api.patched))
	}
	if api.patched[0]["content"] != "9.9.9.9" {
		t.Errorf("content = %v, want 9.9.9.9", api.patched[0]["content"])
	}
	if _, ok := api.patched[0]["proxied"]; ok {
		t.Errorf("expected the proxied flag to stay untouched, got %+v", api.patched[0])
	}
}

func TestRun_CloudflareSkipsWhenStateMatches(t *testing.T) {
	api := &cfAPI{}
	u, sm := setupCloudflare(t, "5.6.7.8", api, nil)

	st, err := sm.Load()
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if err := sm.MarkUpdatedFor(st, config.ProviderCloudflare, "5.6.7.8", "existing-record-id"); err != nil {
		t.Fatalf("mark updated: %v", err)
	}

	result, err := u.Run(context.Background(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != updater.ActionNoChange {
		t.Errorf("action = %q, want %q", result.Action, updater.ActionNoChange)
	}
	if api.writes != 0 {
		t.Errorf("got %d API writes, want 0", api.writes)
	}
}

func TestRun_CloudflareIgnoresHetznerState(t *testing.T) {
	api := &cfAPI{}
	u, sm := setupCloudflare(t, "5.6.7.8", api, nil)

	// The Hetzner provider already knows the IP - the Cloudflare record
	// still has to be synchronised.
	st, err := sm.Load()
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if err := sm.MarkUpdatedFor(st, config.ProviderHetzner, "5.6.7.8", "hetzner-rec"); err != nil {
		t.Fatalf("mark updated: %v", err)
	}

	result, err := u.Run(context.Background(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != updater.ActionCreated {
		t.Errorf("action = %q, want %q", result.Action, updater.ActionCreated)
	}
	if len(api.created) != 1 {
		t.Errorf("got %d create requests, want 1", len(api.created))
	}
}

func TestRun_CloudflareResolvesAndCachesZone(t *testing.T) {
	api := &cfAPI{}
	u, _ := setupCloudflare(t, "5.6.7.8", api, nil)

	if _, err := u.Run(context.Background(), false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The resolved zone ID must be written back to the configuration.
	cfgPath, err := config.DefaultConfigPath()
	if err != nil {
		t.Fatalf("default config path: %v", err)
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}

	var reloaded map[string]any
	if err := json.Unmarshal(data, &reloaded); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	cfBlock, ok := reloaded["cloudflare"].(map[string]any)
	if !ok {
		t.Fatal("expected the cloudflare block to be persisted")
	}
	if cfBlock["zone_id"] != cfZoneID {
		t.Errorf("persisted zone id = %v, want %q", cfBlock["zone_id"], cfZoneID)
	}
}

func TestRun_CloudflareReportsAPIErrors(t *testing.T) {
	api := &cfAPI{failWith: http.StatusForbidden}
	u, _ := setupCloudflare(t, "5.6.7.8", api, nil)

	_, err := u.Run(context.Background(), false)
	if err == nil {
		t.Fatal("expected an error for a failing API")
	}
	if !strings.Contains(err.Error(), "access denied") {
		t.Errorf("expected an access denied error, got: %v", err)
	}
}

func TestRun_CloudflareDryRunDoesNotWrite(t *testing.T) {
	api := &cfAPI{}
	u, _ := setupCloudflareWithOptions(t, "5.6.7.8", api, nil, true)

	result, err := u.Run(context.Background(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.DryRun {
		t.Error("expected DryRun=true in the result")
	}
	if result.Action != updater.ActionCreated {
		t.Errorf("action = %q, want %q", result.Action, updater.ActionCreated)
	}
	if api.writes != 0 {
		t.Errorf("got %d API writes, want 0", api.writes)
	}
}

func TestRun_CloudflareRecordsTheFailure(t *testing.T) {
	api := &cfAPI{failWith: http.StatusForbidden}
	u, sm := setupCloudflare(t, "5.6.7.8", api, nil)

	if _, err := u.Run(context.Background(), false); err == nil {
		t.Fatal("expected an error for a failing API")
	}

	st, err := sm.Load()
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if got := sm.LastErrorFor(st, config.ProviderCloudflare); got == "" {
		t.Error("expected the failed run to be recorded in the state")
	}
}
