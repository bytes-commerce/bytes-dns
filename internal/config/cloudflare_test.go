package config_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/bytes-commerce/bytes-dns/internal/config"
)

func TestLoad_CloudflareBlock(t *testing.T) {
	base := validBase()
	base["cloudflare"] = map[string]any{
		"api_token": "cf-token-xyz789",
		"zone":      "example.com",
		"record":    "home.example.com",
		"proxied":   false,
	}

	path := writeConfig(t, base, 0o600)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Cloudflare == nil {
		t.Fatal("expected the cloudflare block to be parsed")
	}
	if cfg.Cloudflare.APIToken != "cf-token-xyz789" {
		t.Errorf("cloudflare api token = %q, want %q", cfg.Cloudflare.APIToken, "cf-token-xyz789")
	}
	if cfg.Cloudflare.Proxied == nil {
		t.Fatal("expected proxied to be set")
	}
	if *cfg.Cloudflare.Proxied {
		t.Error("expected proxied to be false")
	}
}

func TestLoad_CloudflareProxiedUnset(t *testing.T) {
	base := validBase()
	base["cloudflare"] = map[string]any{
		"api_token": "cf-token-xyz789",
	}

	path := writeConfig(t, base, 0o600)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Cloudflare.Proxied != nil {
		t.Errorf("expected proxied to stay unset, got %v", *cfg.Cloudflare.Proxied)
	}
}

func TestLoad_CloudflareWithoutHetznerToken(t *testing.T) {
	path := writeConfig(t, map[string]any{
		"zone":   "example.com",
		"record": "home.example.com",
		"cloudflare": map[string]any{
			"api_token": "cf-token-xyz789",
		},
	}, 0o600)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	providers := cfg.ProviderConfigs()
	if len(providers) != 1 {
		t.Fatalf("got %d providers, want 1", len(providers))
	}
	if providers[0].Key != config.ProviderCloudflare {
		t.Errorf("provider key = %q, want %q", providers[0].Key, config.ProviderCloudflare)
	}
}

func TestLoad_CloudflareBlockWithoutToken(t *testing.T) {
	base := validBase()
	base["cloudflare"] = map[string]any{"zone": "example.com"}

	path := writeConfig(t, base, 0o600)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected an error for a cloudflare block without a token")
	}
	if !strings.Contains(err.Error(), "cloudflare.api_token") {
		t.Errorf("expected the error to mention cloudflare.api_token, got: %v", err)
	}
}

func TestLoad_CloudflareTTLTooLow(t *testing.T) {
	base := validBase()
	base["cloudflare"] = map[string]any{
		"api_token": "cf-token-xyz789",
		"ttl":       30,
	}

	path := writeConfig(t, base, 0o600)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected an error for a cloudflare ttl below 60")
	}
	if !strings.Contains(err.Error(), "cloudflare.ttl") {
		t.Errorf("expected the error to mention cloudflare.ttl, got: %v", err)
	}
}

func TestLoad_CloudflareRecordOutsideZone(t *testing.T) {
	base := validBase()
	base["cloudflare"] = map[string]any{
		"api_token": "cf-token-xyz789",
		"zone":      "example.com",
		"record":    "home.other.com",
	}

	path := writeConfig(t, base, 0o600)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected an error for a record outside the cloudflare zone")
	}
	if !strings.Contains(err.Error(), "cloudflare.record") {
		t.Errorf("expected the error to mention cloudflare.record, got: %v", err)
	}
}

func TestProviderConfigs_InheritsZoneAndRecord(t *testing.T) {
	base := validBase()
	base["cloudflare"] = map[string]any{
		"api_token": "cf-token-xyz789",
	}

	path := writeConfig(t, base, 0o600)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	providers := cfg.ProviderConfigs()
	if len(providers) != 2 {
		t.Fatalf("got %d providers, want 2", len(providers))
	}

	if providers[0].Key != config.ProviderHetzner {
		t.Errorf("first provider = %q, want %q", providers[0].Key, config.ProviderHetzner)
	}
	if providers[1].Key != config.ProviderCloudflare {
		t.Errorf("second provider = %q, want %q", providers[1].Key, config.ProviderCloudflare)
	}

	cf := providers[1]
	if cf.Zone != "example.com" {
		t.Errorf("cloudflare zone = %q, want %q", cf.Zone, "example.com")
	}
	if cf.Record != "home.example.com" {
		t.Errorf("cloudflare record = %q, want %q", cf.Record, "home.example.com")
	}
	if cf.TTL != config.DefaultTTL {
		t.Errorf("cloudflare ttl = %d, want %d", cf.TTL, config.DefaultTTL)
	}
	if cf.RecordType != "A" {
		t.Errorf("cloudflare record type = %q, want A", cf.RecordType)
	}
}

func TestProviderConfigs_CloudflareOverrides(t *testing.T) {
	base := validBase()
	base["ttl"] = 300
	base["cloudflare"] = map[string]any{
		"api_token": "cf-token-xyz789",
		"zone":      "other.com",
		"record":    "home.other.com",
		"ttl":       120,
	}

	path := writeConfig(t, base, 0o600)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	providers := cfg.ProviderConfigs()
	cf := providers[1]
	if cf.Zone != "other.com" {
		t.Errorf("cloudflare zone = %q, want %q", cf.Zone, "other.com")
	}
	if cf.Record != "home.other.com" {
		t.Errorf("cloudflare record = %q, want %q", cf.Record, "home.other.com")
	}
	if cf.TTL != 120 {
		t.Errorf("cloudflare ttl = %d, want 120", cf.TTL)
	}
	if providers[0].TTL != 300 {
		t.Errorf("hetzner ttl = %d, want 300", providers[0].TTL)
	}
}

func TestSetZone_Cloudflare(t *testing.T) {
	base := validBase()
	base["cloudflare"] = map[string]any{
		"api_token": "cf-token-xyz789",
	}

	path := writeConfig(t, base, 0o600)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := cfg.SetZone(config.ProviderCloudflare, "023e105f4ecef8ad9ca31a8372d0c353", "example.com"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Cloudflare.ZoneID != "023e105f4ecef8ad9ca31a8372d0c353" {
		t.Errorf("cloudflare zone id = %q, want the resolved id", cfg.Cloudflare.ZoneID)
	}

	// The change must be persisted to the file the config was loaded from.
	data, err := os.ReadFile(path)
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
	if cfBlock["zone_id"] != "023e105f4ecef8ad9ca31a8372d0c353" {
		t.Errorf("persisted zone id = %v, want the resolved id", cfBlock["zone_id"])
	}
}
