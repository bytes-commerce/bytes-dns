package state_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/bytes-commerce/bytes-dns/internal/state"
)

func TestLoad_MigratesLegacyState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	legacy := `{"last_ip":"1.2.3.4","last_record_id":"rec-1","last_updated":"2026-01-01T00:00:00Z"}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatalf("write state: %v", err)
	}

	sm := state.New(path)
	st, err := sm.Load()
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}

	if got := sm.LastIPFor(st, "hetzner"); got != "1.2.3.4" {
		t.Errorf("hetzner last IP = %q, want %q", got, "1.2.3.4")
	}
	if got := sm.LastIPFor(st, "cloudflare"); got != "" {
		t.Errorf("cloudflare last IP = %q, want empty", got)
	}
	if st.Providers["hetzner"].LastRecordID != "rec-1" {
		t.Errorf("migrated record id = %q, want %q", st.Providers["hetzner"].LastRecordID, "rec-1")
	}
}

func TestProviderStateIsIsolated(t *testing.T) {
	sm := newManager(t)
	st, err := sm.Load()
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}

	if err := sm.MarkUpdatedFor(st, "hetzner", "1.2.3.4", "hetzner-rec"); err != nil {
		t.Fatalf("mark updated: %v", err)
	}

	if got := sm.LastIPFor(st, "hetzner"); got != "1.2.3.4" {
		t.Errorf("hetzner last IP = %q, want %q", got, "1.2.3.4")
	}
	if got := sm.LastIPFor(st, "cloudflare"); got != "" {
		t.Errorf("cloudflare last IP = %q, want empty", got)
	}

	if err := sm.MarkUpdatedFor(st, "cloudflare", "5.6.7.8", "cf-rec"); err != nil {
		t.Fatalf("mark updated: %v", err)
	}

	if got := sm.LastIPFor(st, "hetzner"); got != "1.2.3.4" {
		t.Errorf("hetzner last IP = %q, want %q", got, "1.2.3.4")
	}
	if got := sm.LastIPFor(st, "cloudflare"); got != "5.6.7.8" {
		t.Errorf("cloudflare last IP = %q, want %q", got, "5.6.7.8")
	}
}

func TestProviderStateSurvivesReload(t *testing.T) {
	sm := newManager(t)
	st, err := sm.Load()
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}

	if err := sm.MarkUpdatedFor(st, "cloudflare", "5.6.7.8", "cf-rec"); err != nil {
		t.Fatalf("mark updated: %v", err)
	}

	reloaded, err := sm.Load()
	if err != nil {
		t.Fatalf("reload failed: %v", err)
	}
	if got := sm.LastIPFor(reloaded, "cloudflare"); got != "5.6.7.8" {
		t.Errorf("cloudflare last IP = %q, want %q", got, "5.6.7.8")
	}
	if got := sm.LastRecordIDFor(reloaded, "cloudflare"); got != "cf-rec" {
		t.Errorf("cloudflare record id = %q, want %q", got, "cf-rec")
	}
}

func TestMarkCheckedForKeepsTheLastIP(t *testing.T) {
	sm := newManager(t)
	st, err := sm.Load()
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}

	if err := sm.MarkUpdatedFor(st, "cloudflare", "5.6.7.8", "cf-rec"); err != nil {
		t.Fatalf("mark updated: %v", err)
	}
	if err := sm.MarkCheckedFor(st, "cloudflare"); err != nil {
		t.Fatalf("mark checked: %v", err)
	}

	if got := sm.LastIPFor(st, "cloudflare"); got != "5.6.7.8" {
		t.Errorf("cloudflare last IP = %q, want %q", got, "5.6.7.8")
	}
}

func TestFlatStateBelongsToThePrimaryProvider(t *testing.T) {
	sm := newManager(t)
	st, err := sm.Load()
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}

	// A Cloudflare only run must not make the primary provider believe the
	// IP is already up to date.
	if err := sm.MarkUpdatedFor(st, "cloudflare", "5.6.7.8", "cf-rec"); err != nil {
		t.Fatalf("mark updated: %v", err)
	}

	if got := sm.LastIPFor(st, "hetzner"); got != "" {
		t.Errorf("hetzner last IP = %q, want empty (a Cloudflare update must not suppress a later Hetzner update)", got)
	}
	if st.LastIP != "" {
		t.Errorf("flat last IP = %q, want empty", st.LastIP)
	}
	if got := sm.LastIPFor(st, "cloudflare"); got != "5.6.7.8" {
		t.Errorf("cloudflare last IP = %q, want %q", got, "5.6.7.8")
	}
}

func TestMarkErrorForKeepsTheLastIP(t *testing.T) {
	sm := newManager(t)
	st, err := sm.Load()
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}

	if err := sm.MarkUpdatedFor(st, "cloudflare", "5.6.7.8", "cf-rec"); err != nil {
		t.Fatalf("mark updated: %v", err)
	}
	if err := sm.MarkErrorFor(st, "cloudflare", errors.New("access denied")); err != nil {
		t.Fatalf("mark error: %v", err)
	}

	if got := sm.LastErrorFor(st, "cloudflare"); got != "access denied" {
		t.Errorf("last error = %q, want %q", got, "access denied")
	}
	if got := sm.LastIPFor(st, "cloudflare"); got != "5.6.7.8" {
		t.Errorf("cloudflare last IP = %q, want %q (a failed run must not drop the known IP)", got, "5.6.7.8")
	}

	// A successful run clears the error again.
	if err := sm.MarkUpdatedFor(st, "cloudflare", "5.6.7.8", "cf-rec"); err != nil {
		t.Fatalf("mark updated: %v", err)
	}
	if got := sm.LastErrorFor(st, "cloudflare"); got != "" {
		t.Errorf("last error = %q, want empty after a successful run", got)
	}
}
