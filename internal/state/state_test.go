package state_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bytesbytes/bytes-dns/internal/state"
)

func newManager(t *testing.T) *state.Manager {
	t.Helper()
	dir := t.TempDir()
	return state.New(filepath.Join(dir, "state.json"))
}

func TestLoad_EmptyOnFirstRun(t *testing.T) {
	sm := newManager(t)
	st, err := sm.Load()
	if err != nil {
		t.Fatalf("expected no error on first run, got: %v", err)
	}
	if st == nil {
		t.Fatal("expected empty state, got nil")
	}
	if st.LastIP != "" {
		t.Errorf("expected empty LastIP, got %q", st.LastIP)
	}
}

func TestSaveAndLoad_RoundTrip(t *testing.T) {
	sm := newManager(t)
	now := time.Now().UTC().Truncate(time.Second)

	original := &state.State{
		LastIP:       "1.2.3.4",
		LastRecordID: "rec-abc123",
		LastUpdated:  now,
		LastChecked:  now,
		LastSyncedIP: "1.2.3.4",
	}

	if err := sm.Save(original); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	loaded, err := sm.Load()
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}

	if loaded.LastIP != original.LastIP {
		t.Errorf("LastIP = %q, want %q", loaded.LastIP, original.LastIP)
	}
	if loaded.LastRecordID != original.LastRecordID {
		t.Errorf("LastRecordID = %q, want %q", loaded.LastRecordID, original.LastRecordID)
	}
	if !loaded.LastUpdated.Equal(original.LastUpdated) {
		t.Errorf("LastUpdated = %v, want %v", loaded.LastUpdated, original.LastUpdated)
	}
}

func TestMarkUpdated(t *testing.T) {
	sm := newManager(t)
	st, _ := sm.Load()

	before := time.Now().UTC()
	if err := sm.MarkUpdated(st, "5.6.7.8", "rec-xyz"); err != nil {
		t.Fatalf("MarkUpdated failed: %v", err)
	}
	after := time.Now().UTC()

	loaded, _ := sm.Load()
	if loaded.LastIP != "5.6.7.8" {
		t.Errorf("LastIP = %q, want %q", loaded.LastIP, "5.6.7.8")
	}
	if loaded.LastRecordID != "rec-xyz" {
		t.Errorf("LastRecordID = %q, want %q", loaded.LastRecordID, "rec-xyz")
	}
	if loaded.LastUpdated.Before(before) || loaded.LastUpdated.After(after) {
		t.Errorf("LastUpdated %v outside expected range [%v, %v]", loaded.LastUpdated, before, after)
	}
}

func TestMarkChecked(t *testing.T) {
	sm := newManager(t)
	st, _ := sm.Load()
	st.LastIP = "9.9.9.9"
	_ = sm.Save(st)

	before := time.Now().UTC()
	_ = sm.MarkChecked(st)
	after := time.Now().UTC()

	loaded, _ := sm.Load()
	if loaded.LastIP != "9.9.9.9" {
		t.Errorf("LastIP should be preserved after MarkChecked, got %q", loaded.LastIP)
	}
	if loaded.LastChecked.Before(before) || loaded.LastChecked.After(after) {
		t.Errorf("LastChecked %v outside expected range", loaded.LastChecked)
	}
}

func TestLoad_CorruptStateReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("{corrupt json"), 0o600); err != nil {
		t.Fatal(err)
	}

	sm := state.New(path)
	st, err := sm.Load()
	if err != nil {
		t.Fatalf("expected graceful recovery from corrupt state, got: %v", err)
	}
	if st.LastIP != "" {
		t.Errorf("expected empty state after corrupt file, got LastIP=%q", st.LastIP)
	}
}

func TestSave_CreatesDirectory(t *testing.T) {
	dir := t.TempDir()
	nestedPath := filepath.Join(dir, "nested", "deep", "state.json")
	sm := state.New(nestedPath)

	st := &state.State{LastIP: "1.1.1.1"}
	if err := sm.Save(st); err != nil {
		t.Fatalf("save to nested path failed: %v", err)
	}

	if _, err := os.Stat(nestedPath); err != nil {
		t.Errorf("state file not created at %s: %v", nestedPath, err)
	}
}

func TestDefaultStatePath(t *testing.T) {
	path := state.DefaultStatePath("/home/user/.bytes-dns")
	expected := "/home/user/.bytes-dns/state.json"
	if path != expected {
		t.Errorf("DefaultStatePath = %q, want %q", path, expected)
	}
}
