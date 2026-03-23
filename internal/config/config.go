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
	DefaultIPSource        = "https://api4.my-ip.io/ip.txt"
	DefaultLogLevel        = "info"
	DefaultRecordType      = "A"
)

type Config struct {
	APIToken        string `json:"api_token"`
	Zone            string `json:"zone"`
	Record          string `json:"record"`
	RecordType      string `json:"record_type"`
	TTL             int    `json:"ttl"`
	IntervalMinutes int    `json:"interval_minutes"`
	IPSource        string `json:"ip_source"`
	LogLevel        string `json:"log_level"`
	AllowPrivateIP  bool   `json:"allow_private_ip"`
	DryRun          bool   `json:"dry_run"`
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
			return nil, fmt.Errorf("config file not found at %s — run 'bytes-dns install' or create it manually", path)
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

	if strings.TrimSpace(cfg.APIToken) == "" {
		errs = append(errs, "api_token is required")
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
		if !strings.HasPrefix(cfg.IPSource, "https://") && !strings.HasPrefix(cfg.IPSource, "http://") {
			errs = append(errs, "ip_source must be a valid http:// or https:// URL")
		}
	}

	if cfg.Zone != "" && cfg.Record != "" {
		record := strings.TrimRight(cfg.Record, ".")
		zone := strings.TrimRight(cfg.Zone, ".")
		if !strings.HasSuffix(strings.ToLower(record), strings.ToLower(zone)) {
			errs = append(errs, fmt.Sprintf("record %q must be within zone %q", cfg.Record, cfg.Zone))
		}
	}

	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func checkFilePermissions(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}
	mode := info.Mode().Perm()
	if mode&0o044 != 0 {
		return fmt.Errorf(
			"config file %s is readable by group or others (mode %04o) — "+
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
	for _, net := range private {
		if net.Contains(ip) {
			return true
		}
	}
	return false
}
