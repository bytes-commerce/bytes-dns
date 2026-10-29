package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/bytes-commerce/bytes-dns/internal/cloudflare"
	"github.com/bytes-commerce/bytes-dns/internal/config"
	"github.com/bytes-commerce/bytes-dns/internal/dns"
	"github.com/bytes-commerce/bytes-dns/internal/installer"
	"github.com/bytes-commerce/bytes-dns/internal/ip"
	"github.com/bytes-commerce/bytes-dns/internal/logger"
	"github.com/bytes-commerce/bytes-dns/internal/selfupdate"
	"github.com/bytes-commerce/bytes-dns/internal/state"
	"github.com/bytes-commerce/bytes-dns/internal/updater"
)

var (
	Version   = "dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)

const usage = `bytes-dns - Dynamic DNS updater for Hetzner DNS and Cloudflare

Usage:
  bytes-dns <command> [flags]

Commands:
  run        Detect public IP and update the configured DNS record(s)
  test       Validate config, detect IP, resolve zone, and preview changes
  status     Show current config, state, and systemd timer status
  setup      Interactive configuration (API Key, Domain, Records)
  install    Install systemd service and timer units
  uninstall  Remove systemd units and binary
  update     Fetch the latest release from GitHub and replace the binary
  version    Print version information

Flags (for 'run'):
  --force     Force DNS update even if cached IP matches
  --once      Run once and exit (default behaviour; alias for clarity)
  --config    Path to config file (default: ~/.bytes-dns/config.json)
  --dry-run   Preview changes without writing to the DNS provider

Global flags:
  --help, -h  Show this help text

Config: ~/.bytes-dns/config.json
Docs:   https://github.com/bytes-commerce/bytes-dns
`

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(1)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "run":
		cmdRun(args)
	case "test":
		cmdTest(args)
	case "status":
		cmdStatus(args)
	case "setup":
		cmdSetup(args)
	case "install":
		cmdInstall()
	case "uninstall":
		cmdUninstall()
	case "update":
		cmdUpdate(args)
	case "version":
		cmdVersion()
	case "--help", "-h", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %q\n\n%s", cmd, usage)
		os.Exit(1)
	}
}

func cmdRun(args []string) {
	var (
		force      bool
		dryRun     bool
		configPath string
	)

	for _, a := range args {
		switch {
		case a == "--force":
			force = true
		case a == "--dry-run":
			dryRun = true
		case a == "--once":
		case strings.HasPrefix(a, "--config="):
			configPath = strings.TrimPrefix(a, "--config=")
		case a == "--help" || a == "-h":
			fmt.Print(usage)
			return
		default:
			fmt.Fprintf(os.Stderr, "unknown flag: %q\n", a)
			os.Exit(1)
		}
	}

	cfg, sm := mustLoadConfig(configPath)
	if dryRun {
		cfg.DryRun = true
	}
	if cfg.DryRun {
		logger.Info("dry-run mode is ACTIVE - no DNS writes will be performed")
	}

	logger.SetLevel(logger.ParseLevel(cfg.LogLevel))

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	providers := cfg.ProviderConfigs()
	if len(providers) == 0 {
		logger.Error("no DNS provider configured - set api_token and/or cloudflare.api_token in the config")
		os.Exit(1)
	}

	// The detector is shared so the public IP is only looked up once.
	detector := ip.NewWithSources(cfg.IPSources())

	for _, prov := range providers {
		client, err := updater.NewClientForProvider(prov)
		if err != nil {
			logger.Error("%v", err)
			os.Exit(1)
		}

		u := updater.NewForProvider(updater.Options{
			Config:   cfg,
			State:    sm,
			Provider: prov,
			Client:   client,
			Detector: detector,
		})

		result, err := u.Run(ctx, force)
		if err != nil {
			logger.Error("[%s] %v", prov.Key, err)
			os.Exit(1)
		}

		switch result.Action {
		case updater.ActionNoChange:
			logger.Info("done [%s] - no change (ip=%s)", prov.Key, result.PublicIP)
		case updater.ActionUpdated:
			if result.DryRun {
				logger.Info("done [%s] - [dry-run] would update to ip=%s", prov.Key, result.PublicIP)
			} else {
				logger.Info("done [%s] - updated dns record to ip=%s", prov.Key, result.PublicIP)
			}
		case updater.ActionCreated:
			if result.DryRun {
				logger.Info("done [%s] - [dry-run] would create record with ip=%s", prov.Key, result.PublicIP)
			} else {
				logger.Info("done [%s] - created dns record with ip=%s", prov.Key, result.PublicIP)
			}
		}
	}
}

func cmdTest(args []string) {
	var configPath string
	for _, a := range args {
		if strings.HasPrefix(a, "--config=") {
			configPath = strings.TrimPrefix(a, "--config=")
		}
	}

	cfg, sm := mustLoadConfig(configPath)
	logger.SetLevel(logger.LevelDebug)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	providers := cfg.ProviderConfigs()
	if len(providers) == 0 {
		fmt.Fprintln(os.Stderr, "ERROR: no DNS provider configured")
		os.Exit(1)
	}

	detector := ip.NewWithSources(cfg.IPSources())

	for _, prov := range providers {
		client, err := updater.NewClientForProvider(prov)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			os.Exit(1)
		}

		u := updater.NewForProvider(updater.Options{
			Config:   cfg,
			State:    sm,
			Provider: prov,
			Client:   client,
			Detector: detector,
		})

		if err := u.Test(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "ERROR [%s]: %v\n", prov.Key, err)
			os.Exit(1)
		}
	}
}

func cmdStatus(args []string) {
	var configPath string
	for _, a := range args {
		if strings.HasPrefix(a, "--config=") {
			configPath = strings.TrimPrefix(a, "--config=")
		}
	}

	resolvedPath := configPath
	if resolvedPath == "" {
		p, err := config.DefaultConfigPath()
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			os.Exit(1)
		}
		resolvedPath = p
	}

	cfg, err := config.Load(resolvedPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR loading config: %v\n", err)
		os.Exit(1)
	}

	// The state is always kept next to the default config directory, no
	// matter which config file is read.
	stateDir, err := config.ConfigDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}
	sm := state.New(state.DefaultStatePath(stateDir))
	st, err := sm.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARN: cannot load state: %v\n", err)
	}

	fmt.Printf("bytes-dns status\n")
	fmt.Printf("  version        : %s (%s)\n", Version, Commit)
	fmt.Printf("  config file    : %s\n", resolvedPath)
	fmt.Printf("  zone           : %s\n", cfg.Zone)
	fmt.Printf("  record         : %s %s (label: %q)\n", cfg.RecordType, cfg.Record, cfg.RecordLabel())
	fmt.Printf("  ip source      : %s\n", cfg.IPSource)
	fmt.Printf("  interval       : %d minutes\n", cfg.IntervalMinutes)
	fmt.Printf("  dry_run        : %v\n", cfg.DryRun)
	fmt.Printf("  log level      : %s\n", cfg.LogLevel)

	providers := cfg.ProviderConfigs()
	fmt.Printf("  providers      : %d\n", len(providers))
	for _, prov := range providers {
		zoneID := prov.ZoneID
		if zoneID == "" {
			zoneID = "(unresolved)"
		}
		fmt.Printf("    - %s\n", prov.Key)
		fmt.Printf("        zone     : %s (id: %s)\n", prov.Zone, zoneID)
		fmt.Printf("        record   : %s %s\n", prov.RecordType, prov.Record)
		fmt.Printf("        ttl      : %d\n", prov.TTL)
		if prov.Key == config.ProviderCloudflare {
			proxied := true
			if prov.Proxied != nil {
				proxied = *prov.Proxied
			}
			fmt.Printf("        proxied  : %v (new records only)\n", proxied)
		}
		if st != nil {
			if lastErr := sm.LastErrorFor(st, prov.Key); lastErr != "" {
				fmt.Printf("        last error: %s\n", truncateError(lastErr))
			}
			if lastIP := sm.LastIPFor(st, prov.Key); lastIP != "" {
				fmt.Printf("        last IP  : %s\n", lastIP)
			} else {
				fmt.Printf("        last IP  : (not yet cached)\n")
			}
			if lastUpdated := sm.LastUpdatedFor(st, prov.Key); !lastUpdated.IsZero() {
				fmt.Printf("        updated  : %s\n", lastUpdated.UTC().Format(time.RFC3339))
			}
		}
	}

	// The flat state fields belong to the primary provider, so they are only
	// meaningful when it is configured.
	hetznerConfigured := false
	for _, prov := range providers {
		if prov.Key == config.ProviderHetzner {
			hetznerConfigured = true
			break
		}
	}

	if st != nil && hetznerConfigured {
		if st.LastIP != "" {
			fmt.Printf("  last known IP  : %s\n", st.LastIP)
		} else {
			fmt.Printf("  last known IP  : (not yet cached)\n")
		}
		if !st.LastUpdated.IsZero() {
			fmt.Printf("  last updated   : %s\n", st.LastUpdated.UTC().Format(time.RFC3339))
		}
		if !st.LastChecked.IsZero() {
			fmt.Printf("  last checked   : %s\n", st.LastChecked.UTC().Format(time.RFC3339))
		}
	}

	printSystemdStatus()
}

func printSystemdStatus() {
	if runtime.GOOS != "linux" {
		return
	}
	fmt.Println()
	out, err := exec.Command("systemctl", "is-active", "bytes-dns.timer").Output()
	if err != nil {
		fmt.Println("  systemd timer  : not installed or not active")
		return
	}
	fmt.Printf("  systemd timer  : %s\n", strings.TrimSpace(string(out)))

	out2, err2 := exec.Command("systemctl", "status", "--no-pager", "--lines=0", "bytes-dns.timer").Output()
	if err2 == nil {
		for _, line := range strings.Split(string(out2), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "Trigger:") || strings.HasPrefix(trimmed, "Active:") {
				fmt.Printf("    %s\n", trimmed)
			}
		}
	}
}

func cmdSetup(args []string) {
	var configPath string
	for _, a := range args {
		if strings.HasPrefix(a, "--config=") {
			configPath = strings.TrimPrefix(a, "--config=")
		}
	}

	if configPath == "" {
		var err error
		configPath, err = config.DefaultConfigPath()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	}

	cfg, _ := config.Load(configPath)
	if cfg == nil {
		cfg = &config.Config{
			TTL:             config.DefaultTTL,
			IntervalMinutes: config.DefaultIntervalMinutes,
			IPSource:        config.DefaultIPSource,
			LogLevel:        config.DefaultLogLevel,
			RecordType:      config.DefaultRecordType,
		}
	}

	fmt.Println("=== bytes-dns setup ===")

	// --- Hetzner ---
	if cfg.APIToken == "" {
		fmt.Print("Hetzner API Token (leave empty to skip Hetzner): ")
		var token string
		_, _ = fmt.Scanln(&token)
		cfg.APIToken = token
	} else {
		fmt.Printf("Hetzner API Token (current: %s...): ", truncate(cfg.APIToken, 5))
		var newToken string
		_, _ = fmt.Scanln(&newToken)
		if newToken != "" {
			cfg.APIToken = newToken
		}
	}

	fmt.Print("Target Domain (e.g. home.example.com): ")
	var record string
	_, _ = fmt.Scanln(&record)
	if record != "" {
		cfg.Record = record
	}

	fmt.Print("Zone Name (e.g. example.com): ")
	var inputZone string
	_, _ = fmt.Scanln(&inputZone)
	if inputZone != "" {
		cfg.Zone = inputZone
	}

	if cfg.Zone == "" {
		fmt.Print("Zone Name (e.g. example.com): ")
		_, _ = fmt.Scanln(&cfg.Zone)
	}

	ctx := context.Background()

	if cfg.APIToken != "" {
		if err := setupHetznerZone(ctx, cfg); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	} else {
		fmt.Println("Skipping Hetzner zone lookup - no Hetzner API token configured.")
	}

	// --- Cloudflare ---
	if err := setupCloudflare(ctx, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Record Type (A or AAAA) [current: %s]: ", cfg.RecordType)
	var inputType string
	_, _ = fmt.Scanln(&inputType)
	if inputType != "" {
		cfg.RecordType = strings.ToUpper(inputType)
	}

	if cfg.RecordType == "" {
		cfg.RecordType = "A"
	}

	if err := cfg.Save(configPath); err != nil {
		fmt.Fprintf(os.Stderr, "Error saving config: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Config saved to %s\n", configPath)
	fmt.Println("You can now run 'bytes-dns test' to verify the connection.")
}

// setupHetznerZone resolves the Hetzner zone and offers to create it when it
// does not exist yet.
func setupHetznerZone(ctx context.Context, cfg *config.Config) error {
	dnsClient := dns.New(cfg.APIToken)

	zone, err := dnsClient.FindZoneByRecord(ctx, cfg.Record)
	if err != nil {
		zone, err = dnsClient.FindZone(ctx, cfg.Zone)
	}

	if err != nil {
		fmt.Printf("Zone %q not found. Do you want to create it? (y/n): ", cfg.Zone)
		var confirm string
		_, _ = fmt.Scanln(&confirm)
		if !strings.EqualFold(confirm, "y") {
			return fmt.Errorf("setup cancelled: Hetzner zone %q not found", cfg.Zone)
		}
		if !strings.Contains(cfg.Zone, ".") {
			return fmt.Errorf("%q does not look like a valid zone name (must contain at least one dot)", cfg.Zone)
		}
		zone, err = dnsClient.CreateZone(ctx, cfg.Zone, cfg.TTL)
		if err != nil {
			return fmt.Errorf("creating zone: %w", err)
		}
		fmt.Printf("Zone %q created successfully (ID: %s).\n", zone.Name, zone.ID)
	}

	if zone == nil {
		return fmt.Errorf("no zone resolved")
	}

	cfg.Zone = zone.Name
	cfg.ZoneID = string(zone.ID)
	fmt.Printf("Using Zone: %s (ID: %s)\n", zone.Name, cfg.ZoneID)

	return nil
}

// setupCloudflare asks for the optional Cloudflare settings and validates
// them against the Cloudflare API.
func setupCloudflare(ctx context.Context, cfg *config.Config) error {
	if cfg.Cloudflare != nil && cfg.Cloudflare.APIToken != "" {
		fmt.Printf("Cloudflare API Token (current: %s...): ", truncate(cfg.Cloudflare.APIToken, 5))
		var token string
		_, _ = fmt.Scanln(&token)
		if token != "" {
			cfg.Cloudflare.APIToken = token
		}
	} else {
		fmt.Print("Configure Cloudflare as well? (y/n): ")
		var answer string
		_, _ = fmt.Scanln(&answer)
		if !strings.EqualFold(answer, "y") {
			return nil
		}

		fmt.Print("Cloudflare API Token (needs Zone:Read + Zone:DNS:Edit): ")
		var token string
		_, _ = fmt.Scanln(&token)
		if token == "" {
			fmt.Println("No Cloudflare API token given - skipping Cloudflare.")
			return nil
		}
		cfg.Cloudflare = &config.CloudflareConfig{APIToken: token}
	}

	if cfg.Cloudflare == nil {
		return nil
	}

	cf := cfg.Cloudflare

	fmt.Printf("Cloudflare Zone (e.g. example.com) [current: %s]: ", defaultValue(cf.Zone, cfg.Zone))
	var zoneName string
	_, _ = fmt.Scanln(&zoneName)
	if zoneName != "" {
		cf.Zone = zoneName
	}
	if cf.Zone == "" {
		cf.Zone = cfg.Zone
	}

	fmt.Printf("Cloudflare Record (e.g. home.example.com) [current: %s]: ", defaultValue(cf.Record, cfg.Record))
	var recordName string
	_, _ = fmt.Scanln(&recordName)
	if recordName != "" {
		cf.Record = recordName
	}
	if cf.Record == "" {
		cf.Record = cfg.Record
	}

	cfClient := cloudflare.New(cloudflare.Options{APIToken: cf.APIToken, Zone: cf.Zone})
	zone, err := cfClient.FindZone(ctx, cf.Zone)
	if err != nil {
		return fmt.Errorf("zone %q not found in Cloudflare (Cloudflare zones cannot be created by bytes-dns): %w", cf.Zone, err)
	}
	cf.Zone = zone.Name
	cf.ZoneID = string(zone.ID)
	fmt.Printf("Using Cloudflare Zone: %s (ID: %s)\n", cf.Zone, cf.ZoneID)

	proxied := true
	if cf.Proxied != nil {
		proxied = *cf.Proxied
	}
	fmt.Printf("Proxy new records through Cloudflare? (Y/n) [current: %v]: ", proxied)
	var proxyAnswer string
	_, _ = fmt.Scanln(&proxyAnswer)
	switch strings.ToLower(strings.TrimSpace(proxyAnswer)) {
	case "n", "no":
		value := false
		cf.Proxied = &value
	case "y", "yes":
		value := true
		cf.Proxied = &value
	}

	return nil
}

func cmdInstall() {
	inst, err := buildInstaller()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}
	if err := inst.Install(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("bytes-dns installed successfully.\n")
	fmt.Printf("  Binary:    %s\n", inst.BinaryPath)
	fmt.Printf("  Units:     %s\n", inst.SystemdDir)
	fmt.Printf("  Timer:     bytes-dns@%s.timer (active)\n", inst.User)

	// If no config exists, suggest running setup.
	cfgPath, _ := config.DefaultConfigPath()
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		fmt.Println()
		fmt.Println("NOTE: no config file found. Run 'bytes-dns setup' to create one.")
	}
}

func cmdUninstall() {
	inst, err := buildInstaller()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}
	if err := inst.Uninstall(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("bytes-dns uninstalled.")
}

func cmdUpdate(args []string) {
	var (
		check bool
		force bool
	)
	for _, a := range args {
		switch {
		case a == "--check":
			check = true
		case a == "--force":
			force = true
		case a == "--help" || a == "-h":
			fmt.Print(`bytes-dns update - fetch and apply the latest release

Usage:
  bytes-dns update [--check] [--force]

Flags:
  --check   Only check whether an update is available; do not download or apply.
  --force   Re-install the current version even if it matches the latest.
`)
			return
		default:
			fmt.Fprintf(os.Stderr, "unknown flag: %q\n", a)
			os.Exit(2)
		}
	}

	u := selfupdate.New(selfupdate.Options{
		RepoOwner: "bytes-commerce",
		RepoName:  "bytes-dns",
		Version:   Version,
	})
	if err := u.RootCheck(); err != nil {
		fmt.Fprintln(os.Stderr, "ERROR: bytes-dns update must be run as root (sudo bytes-dns update)")
		os.Exit(2)
	}

	// Locate the running binary's directory.
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: cannot determine executable path: %v\n", err)
		os.Exit(2)
	}
	u.BinaryDir = filepath.Dir(exe)

	// Determine the user (the same logic as cmdInstall).
	user := os.Getenv("SUDO_USER")
	if user == "" {
		user = os.Getenv("USER")
	}
	if user == "" || user == "root" {
		user = "bytes-dns"
	}
	u.User = user

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if check {
		result, err := u.Check(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			os.Exit(2)
		}
		fmt.Printf("current: %s, latest: %s\n", result.Current, result.Latest)
		if result.UpdateAvailable {
			os.Exit(1)
		}
		return
	}

	result, err := u.Update(ctx, selfupdate.UpdateOptions{Force: force})
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(2)
	}

	switch result.Action {
	case selfupdate.ActionNoChange:
		fmt.Printf("already on v%s, nothing to do\n", result.Current)
	case selfupdate.ActionUpdated:
		fmt.Printf("updated from v%s to v%s\n", result.Current, result.Latest)
	}
}

func buildInstaller() (*installer.Installer, error) {
	// Determine the user the timer will run as.
	user := os.Getenv("SUDO_USER")
	if user == "" {
		// Fall back to invoking user by uid lookup.
		user = os.Getenv("USER")
	}
	if user == "" || user == "root" {
		user = "bytes-dns"
	}

	// Determine config dir.
	configDir, err := config.ConfigDir()
	if err != nil {
		return nil, err
	}

	// Read interval from config; default 5.
	intervalMins := config.DefaultIntervalMinutes
	cfgPath, err := config.DefaultConfigPath()
	if err == nil {
		if cfg, err := config.Load(cfgPath); err == nil && cfg != nil && cfg.IntervalMinutes > 0 {
			intervalMins = cfg.IntervalMinutes
		}
	}

	// Source binary — the running binary.
	src, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("cannot determine executable path: %w", err)
	}

	return installer.New(installer.Options{
		SourceBinary: src,
		BinaryPath:   "/usr/local/bin/bytes-dns",
		SystemdDir:   "/etc/systemd/system",
		ConfigDir:    configDir,
		User:         user,
		IntervalMins: intervalMins,
	}), nil
}

func cmdVersion() {
	fmt.Printf("bytes-dns %s\n", Version)
	fmt.Printf("  commit    : %s\n", Commit)
	fmt.Printf("  built     : %s\n", BuildDate)
	fmt.Printf("  go        : %s\n", runtime.Version())
	fmt.Printf("  os/arch   : %s/%s\n", runtime.GOOS, runtime.GOARCH)
}

func mustLoadConfig(path string) (*config.Config, *state.Manager) {
	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n\n", err)
		fmt.Fprintf(os.Stderr, "Run 'bytes-dns test' to diagnose configuration issues.\n")
		fmt.Fprintf(os.Stderr, "See the example config: examples/config.json\n")
		os.Exit(1)
	}

	dir, err := config.ConfigDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}

	sm := state.New(state.DefaultStatePath(dir))
	return cfg, sm
}

// truncate shortens a secret for display purposes.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// truncateError shortens a stored error message for display.
func truncateError(s string) string {
	const maxLen = 120
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "…"
}

// defaultValue returns value or fallback when value is empty.
func defaultValue(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}
