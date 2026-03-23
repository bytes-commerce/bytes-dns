package config_test

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/bytesbytes/bytes-dns/internal/config"
)

func writeConfig(t *testing.T, cfg map[string]any, perm os.FileMode) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(path, data, perm); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func validBase() map[string]any {
	return map[string]any{
		"api_token": "test-token-abc123",
		"zone":      "example.com",
		"record":    "home.example.com",
	}
}

func TestLoad_Valid(t *testing.T) {
	path := writeConfig(t, validBase(), 0o600)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.APIToken != "test-token-abc123" {
		t.Errorf("APIToken = %q, want %q", cfg.APIToken, "test-token-abc123")
	}
	if cfg.Zone != "example.com" {
		t.Errorf("Zone = %q, want %q", cfg.Zone, "example.com")
	}
	if cfg.Record != "home.example.com" {
		t.Errorf("Record = %q, want %q", cfg.Record, "home.example.com")
	}
}

func TestLoad_Defaults(t *testing.T) {
	path := writeConfig(t, validBase(), 0o600)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.TTL != config.DefaultTTL {
		t.Errorf("TTL = %d, want %d", cfg.TTL, config.DefaultTTL)
	}
	if cfg.IntervalMinutes != config.DefaultIntervalMinutes {
		t.Errorf("IntervalMinutes = %d, want %d", cfg.IntervalMinutes, config.DefaultIntervalMinutes)
	}
	if cfg.IPSource != config.DefaultIPSource {
		t.Errorf("IPSource = %q, want %q", cfg.IPSource, config.DefaultIPSource)
	}
	if cfg.LogLevel != config.DefaultLogLevel {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, config.DefaultLogLevel)
	}
	if cfg.RecordType != config.DefaultRecordType {
		t.Errorf("RecordType = %q, want %q", cfg.RecordType, config.DefaultRecordType)
	}
}

func TestLoad_MissingFile(t *testing.T) {
	_, err := config.Load("/nonexistent/path/config.json")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestLoad_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte("{invalid json}"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

func TestLoad_WorldReadable(t *testing.T) {
	path := writeConfig(t, validBase(), 0o644)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error for world-readable config, got nil")
	}
}

func TestLoad_GroupReadable(t *testing.T) {
	path := writeConfig(t, validBase(), 0o640)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error for group-readable config, got nil")
	}
}

func TestLoad_MissingAPIToken(t *testing.T) {
	m := validBase()
	delete(m, "api_token")
	path := writeConfig(t, m, 0o600)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected validation error for missing api_token, got nil")
	}
}

func TestLoad_MissingZone(t *testing.T) {
	m := validBase()
	delete(m, "zone")
	path := writeConfig(t, m, 0o600)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected validation error for missing zone, got nil")
	}
}

func TestLoad_MissingRecord(t *testing.T) {
	m := validBase()
	delete(m, "record")
	path := writeConfig(t, m, 0o600)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected validation error for missing record, got nil")
	}
}

func TestLoad_RecordOutsideZone(t *testing.T) {
	m := validBase()
	m["record"] = "other.org"
	path := writeConfig(t, m, 0o600)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected validation error for record outside zone, got nil")
	}
}

func TestLoad_InvalidRecordType(t *testing.T) {
	m := validBase()
	m["record_type"] = "MX"
	path := writeConfig(t, m, 0o600)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected validation error for invalid record_type, got nil")
	}
}

func TestLoad_ValidAAAA(t *testing.T) {
	m := validBase()
	m["record_type"] = "AAAA"
	path := writeConfig(t, m, 0o600)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error for AAAA record type: %v", err)
	}
	if cfg.RecordType != "AAAA" {
		t.Errorf("RecordType = %q, want AAAA", cfg.RecordType)
	}
}

func TestLoad_NegativeTTL(t *testing.T) {
	m := validBase()
	m["ttl"] = -1
	path := writeConfig(t, m, 0o600)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected validation error for negative ttl, got nil")
	}
}

func TestLoad_InvalidIPSource(t *testing.T) {
	m := validBase()
	m["ip_source"] = "ftp://not-valid.example.com"
	path := writeConfig(t, m, 0o600)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected validation error for invalid ip_source scheme, got nil")
	}
}

var recordLabelTests = []struct {
	zone   string
	record string
	want   string
}{
	{"example.com", "home.example.com", "home"},
	{"example.com", "example.com", "@"},
	{"example.com", "deep.sub.example.com", "deep.sub"},
	{"example.com", "EXAMPLE.COM", "@"},
	{"example.com", "HOME.Example.Com", "HOME"},
}

func TestRecordLabel(t *testing.T) {
	for _, tc := range recordLabelTests {
		t.Run(tc.record+"_in_"+tc.zone, func(t *testing.T) {
			m := validBase()
			m["zone"] = tc.zone
			m["record"] = tc.record
			path := writeConfig(t, m, 0o600)
			cfg, err := config.Load(path)
			if err != nil {
				t.Fatalf("load failed: %v", err)
			}
			got := cfg.RecordLabel()
			if got != tc.want {
				t.Errorf("RecordLabel() = %q, want %q", got, tc.want)
			}
		})
	}
}

var privateIPTests = []struct {
	ip      string
	private bool
}{
	{"10.0.0.1", true},
	{"10.255.255.255", true},
	{"172.16.0.1", true},
	{"172.31.255.255", true},
	{"192.168.1.1", true},
	{"127.0.0.1", true},
	{"169.254.1.1", true},
	{"100.64.0.1", true},
	{"8.8.8.8", false},
	{"203.0.113.1", false},
	{"1.1.1.1", false},
	{"::1", true},
	{"fc00::1", true},
	{"fe80::1", true},
	{"2001:db8::1", false},
}

func TestIsPrivateIP(t *testing.T) {
	for _, tc := range privateIPTests {
		t.Run(tc.ip, func(t *testing.T) {
			ip := net.ParseIP(tc.ip)
			if ip == nil {
				t.Fatalf("failed to parse IP %q", tc.ip)
			}
			got := config.IsPrivateIP(ip)
			if got != tc.private {
				t.Errorf("IsPrivateIP(%q) = %v, want %v", tc.ip, got, tc.private)
			}
		})
	}
}
