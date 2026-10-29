package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
)

const (
	DefaultTTL             = 60
	DefaultIntervalMinutes = 5
	// DefaultIPSource is the first URL tried; the rest are fallbacks.
	DefaultIPSource   = "https://api4.my-ip.io/ip.txt,https://ifconfig.co/ip,https://checkip.amazonaws.com"
	DefaultLogLevel   = "info"
	DefaultRecordType = "A"

	// ProviderHetzner and ProviderCloudflare are the keys used to address a
	// provider in the configuration and in the local state file.
	ProviderHetzner    = "hetzner"
	ProviderCloudflare = "cloudflare"

	// CloudflareMinTTL is the smallest TTL Cloudflare accepts for a record
	// (1 means "automatic").
	CloudflareMinTTL = 60
)

type Config struct {
	APIToken        string `json:"api_token"`
	Zone            string `json:"zone"`
	ZoneID          string `json:"zone_id,omitempty"`
	Record          string `json:"record"`
	RecordType      string `json:"record_type"`
	TTL             int    `json:"ttl"`
	IntervalMinutes int    `json:"interval_minutes"`
	IPSource        string `json:"ip_source"`
	LogLevel        string `json:"log_level"`
	AllowPrivateIP  bool   `json:"allow_private_ip"`
	DryRun          bool   `json:"dry_run"`

	// Cloudflare is optional. When it carries an API token, the record is
	// synchronised with Cloudflare in addition to the primary provider.
	Cloudflare *CloudflareConfig `json:"cloudflare,omitempty"`

	// path remembers where the config was loaded from so resolved zone IDs
	// can be written back to the same file.
	path string
}

// CloudflareConfig holds the optional Cloudflare settings.
type CloudflareConfig struct {
	// APIToken is a Cloudflare API token with the "Zone:Read" and
	// "Zone:DNS:Edit" permissions.
	APIToken string `json:"api_token"`
	// Zone is the Cloudflare zone, e.g. "example.com". Defaults to the
	// top level zone.
	Zone string `json:"zone,omitempty"`
	// ZoneID is the Cloudflare zone ID. It is looked up once and cached.
	ZoneID string `json:"zone_id,omitempty"`
	// Record is the fully qualified record name. Defaults to the top level
	// record.
	Record string `json:"record,omitempty"`
	// TTL overrides the top level TTL. Cloudflare only accepts 1
	// (automatic) or 60 seconds and above.
	TTL int `json:"ttl,omitempty"`
	// Proxied controls whether newly created records are routed through
	// Cloudflare. Unset means proxied, an explicit false creates a DNS-only
	// record. Existing records are never changed.
	Proxied *bool `json:"proxied,omitempty"`
}

// ProviderConfig is the provider neutral view of a provider configuration.
type ProviderConfig struct {
	// Key identifies the provider, e.g. "hetzner" or "cloudflare".
	Key string
	// APIToken is the credential used for the provider.
	APIToken string
	// Zone is the zone name, e.g. "example.com".
	Zone string
	// ZoneID is the cached zone ID. It may be empty.
	ZoneID string
	// Record is the fully qualified record name, e.g. "home.example.com".
	Record string
	// RecordType is "A" or "AAAA".
	RecordType string
	// TTL is the record TTL in seconds.
	TTL int
	// Proxied is the Cloudflare proxy setting. It is nil for providers that
	// do not support it.
	Proxied *bool
}

func (c *Config) Save(path string) error {
	if path == "" {
		if c.path != "" {
			path = c.path
		} else {
			var err error
			path, err = DefaultConfigPath()
			if err != nil {
				return err
			}
		}
	}

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling config: %w", err)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}

	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("writing config file: %w", err)
	}

	return nil
}

func ConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	return filepath.Join(home, ".bytes-dns"), nil
}

func DefaultConfigPath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

func Load(path string) (*Config, error) {
	if path == "" {
		var err error
		path, err = DefaultConfigPath()
		if err != nil {
			return nil, err
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("config file not found at %s - run 'bytes-dns install' or create it manually", path)
		}
		return nil, fmt.Errorf("cannot read config file %s: %w", path, err)
	}

	if err := checkFilePermissions(path); err != nil {
		return nil, err
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("config file %s contains invalid JSON: %w", path, err)
	}
	cfg.path = path

	applyDefaults(&cfg)

	if err := validate(&cfg); err != nil {
		return nil, fmt.Errorf("config validation failed: %w", err)
	}

	return &cfg, nil
}

func (c *Config) RecordLabel() string {
	record := strings.TrimRight(c.Record, ".")
	zone := strings.TrimRight(c.Zone, ".")

	if strings.EqualFold(record, zone) {
		return "@"
	}

	suffix := "." + zone
	if strings.HasSuffix(strings.ToLower(record), strings.ToLower(suffix)) {
		label := record[:len(record)-len(suffix)]
		return label
	}

	return record
}

// IPSources returns the configured IP source URLs in priority order.
// Empty entries are skipped. Returns at least one entry if IPSource is non-empty.
func (c *Config) IPSources() []string {
	if c.IPSource == "" {
		return strings.Split(DefaultIPSource, ",")
	}
	parts := strings.Split(c.IPSource, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return strings.Split(DefaultIPSource, ",")
	}
	return out
}

// ProviderConfigs returns the providers that are configured and should be
// synchronised. The primary provider comes first.
func (c *Config) ProviderConfigs() []ProviderConfig {
	var out []ProviderConfig

	if strings.TrimSpace(c.APIToken) != "" {
		out = append(out, ProviderConfig{
			Key:        ProviderHetzner,
			APIToken:   c.APIToken,
			Zone:       c.Zone,
			ZoneID:     c.ZoneID,
			Record:     c.Record,
			RecordType: c.RecordType,
			TTL:        c.TTL,
		})
	}

	if cf := c.Cloudflare; cf != nil && strings.TrimSpace(cf.APIToken) != "" {
		out = append(out, ProviderConfig{
			Key:        ProviderCloudflare,
			APIToken:   cf.APIToken,
			Zone:       firstNonEmpty(cf.Zone, c.Zone),
			ZoneID:     cf.ZoneID,
			Record:     firstNonEmpty(cf.Record, c.Record),
			RecordType: c.RecordType,
			TTL:        effectiveTTL(cf.TTL, c.TTL),
			Proxied:    cf.Proxied,
		})
	}

	return out
}

// PrimaryProvider returns the configuration of the primary (Hetzner)
// provider. It is used when no explicit provider is given.
func (c *Config) PrimaryProvider() ProviderConfig {
	return ProviderConfig{
		Key:        ProviderHetzner,
		APIToken:   c.APIToken,
		Zone:       c.Zone,
		ZoneID:     c.ZoneID,
		Record:     c.Record,
		RecordType: c.RecordType,
		TTL:        c.TTL,
	}
}

// SetZone stores a resolved zone for the given provider and writes the
// configuration back to disk.
func (c *Config) SetZone(providerKey, zoneID, zoneName string) error {
	switch providerKey {
	case ProviderHetzner:
		c.ZoneID = zoneID
		if zoneName != "" {
			c.Zone = zoneName
		}
	case ProviderCloudflare:
		if c.Cloudflare == nil {
			return errors.New("cloudflare configuration is missing")
		}
		c.Cloudflare.ZoneID = zoneID
		if zoneName != "" {
			c.Cloudflare.Zone = zoneName
		}
	default:
		return fmt.Errorf("unknown provider %q", providerKey)
	}

	return c.Save("")
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// effectiveTTL returns the override when it is set, otherwise the fallback.
func effectiveTTL(override, fallback int) int {
	if override > 0 {
		return override
	}
	return fallback
}

func applyDefaults(cfg *Config) {
	if cfg.TTL == 0 {
		cfg.TTL = DefaultTTL
	}
	if cfg.IntervalMinutes == 0 {
		cfg.IntervalMinutes = DefaultIntervalMinutes
	}
	if cfg.IPSource == "" {
		cfg.IPSource = DefaultIPSource
	}
	if cfg.LogLevel == "" {
		cfg.LogLevel = DefaultLogLevel
	}
	if cfg.RecordType == "" {
		cfg.RecordType = DefaultRecordType
	}
}

func validate(cfg *Config) error {
	var errs []string

	hetznerConfigured := strings.TrimSpace(cfg.APIToken) != ""
	cf := cfg.Cloudflare
	cloudflareConfigured := cf != nil && strings.TrimSpace(cf.APIToken) != ""

	if !hetznerConfigured && !cloudflareConfigured {
		errs = append(errs, "api_token is required (Hetzner) unless a cloudflare.api_token is configured")
	}

	if cf != nil && !cloudflareConfigured {
		errs = append(errs, "cloudflare.api_token is required when a cloudflare block is present")
	}

	if strings.TrimSpace(cfg.Zone) == "" {
		errs = append(errs, "zone is required (e.g. \"example.com\")")
	}

	if strings.TrimSpace(cfg.Record) == "" {
		errs = append(errs, "record is required (e.g. \"home.example.com\")")
	}

	if cfg.RecordType != "A" && cfg.RecordType != "AAAA" {
		errs = append(errs, fmt.Sprintf("record_type must be \"A\" or \"AAAA\", got %q", cfg.RecordType))
	}

	if cfg.TTL < 1 {
		errs = append(errs, "ttl must be >= 1")
	}

	if cfg.IntervalMinutes < 1 {
		errs = append(errs, "interval_minutes must be >= 1")
	}

	if cfg.IPSource != "" {
		first := cfg.IPSources()[0]
		if !strings.HasPrefix(first, "https://") && !strings.HasPrefix(first, "http://") {
			errs = append(errs, "ip_source must be a valid http:// or https:// URL")
		}
		if strings.Contains(first, "://127.0.0.1") || strings.Contains(first, "://localhost") {
			errs = append(errs, "ip_source cannot be a localhost address")
		}
	}

	if cfg.Zone != "" && cfg.Record != "" {
		errs = append(errs, validateRecordInZone(cfg.Record, cfg.Zone, "record", "zone")...)
	}

	if cloudflareConfigured {
		cfZone := firstNonEmpty(cf.Zone, cfg.Zone)
		cfRecord := firstNonEmpty(cf.Record, cfg.Record)

		if cf.TTL != 0 && cf.TTL != 1 && cf.TTL < CloudflareMinTTL {
			errs = append(errs, fmt.Sprintf("cloudflare.ttl must be 1 (automatic) or >= %d, got %d", CloudflareMinTTL, cf.TTL))
		}

		if cfZone != "" && cfRecord != "" {
			errs = append(errs, validateRecordInZone(cfRecord, cfZone, "cloudflare.record", "cloudflare.zone")...)
		}
	}

	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// validateRecordInZone checks that a record name is a subdomain of a zone.
func validateRecordInZone(record, zone, recordLabel, zoneLabel string) []string {
	record = strings.TrimRight(record, ".")
	zone = strings.TrimRight(zone, ".")

	var errs []string
	if !strings.EqualFold(record, zone) && !strings.HasSuffix(strings.ToLower(record), "."+strings.ToLower(zone)) {
		errs = append(errs, fmt.Sprintf("%s %q must be within %s %q", recordLabel, record, zoneLabel, zone))
	}
	if strings.EqualFold(record, zone) {
		errs = append(errs, fmt.Sprintf("%s %q is the zone apex — use a subdomain (e.g. \"home.%s\")", recordLabel, record, zone))
	}
	return errs
}

func checkFilePermissions(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}
	mode := info.Mode().Perm()
	if mode&0o044 != 0 {
		return fmt.Errorf(
			"config file %s is readable by group or others (mode %04o) - "+
				"run: chmod 600 %s",
			path, mode, path,
		)
	}
	return nil
}

func IsPrivateIP(ip net.IP) bool {
	private := []net.IPNet{
		{IP: net.ParseIP("10.0.0.0"), Mask: net.CIDRMask(8, 32)},
		{IP: net.ParseIP("172.16.0.0"), Mask: net.CIDRMask(12, 32)},
		{IP: net.ParseIP("192.168.0.0"), Mask: net.CIDRMask(16, 32)},
		{IP: net.ParseIP("100.64.0.0"), Mask: net.CIDRMask(10, 32)},
		{IP: net.ParseIP("169.254.0.0"), Mask: net.CIDRMask(16, 32)},
		{IP: net.ParseIP("127.0.0.0"), Mask: net.CIDRMask(8, 32)},
		{IP: net.ParseIP("::1"), Mask: net.CIDRMask(128, 128)},
		{IP: net.ParseIP("fc00::"), Mask: net.CIDRMask(7, 128)},
		{IP: net.ParseIP("fe80::"), Mask: net.CIDRMask(10, 128)},
	}
	for _, c := range private {
		if c.Contains(ip) {
			return true
		}
	}
	return false
}
