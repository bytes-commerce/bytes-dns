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

	"github.com/bytes-commerce/bytes-dns/internal/config"
	"github.com/bytes-commerce/bytes-dns/internal/dns"
	"github.com/bytes-commerce/bytes-dns/internal/installer"
	"github.com/bytes-commerce/bytes-dns/internal/logger"
	"github.com/bytes-commerce/bytes-dns/internal/state"
	"github.com/bytes-commerce/bytes-dns/internal/updater"
)

var (
	Version   = "dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)

const usage = `bytes-dns - Dynamic DNS updater for Hetzner DNS

Usage:
  bytes-dns <command> [flags]

Commands:
  run        Detect public IP and update the configured DNS record
  test       Validate config, detect IP, resolve zone, and preview changes
  status     Show current config, cached state, and systemd timer status
  setup      Interactive configuration (API Key, Domain, Records)
  install    Install systemd service and timer units
  uninstall  Remove systemd units and binary
  version    Print version information

Flags (for 'run'):
  --force     Force DNS update even if cached IP matches
  --once      Run once and exit (default behaviour; alias for clarity)
  --config    Path to config file (default: ~/.bytes-dns/config.json)
  --dry-run   Preview changes without writing to Hetzner DNS

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
		logger.Info("dry-run mode is ACTIVE — no DNS writes will be performed")
	}

	logger.SetLevel(logger.ParseLevel(cfg.LogLevel))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	u := updater.New(cfg, sm)
	result, err := u.Run(ctx, force)
	if err != nil {
		logger.Error("%v", err)
		os.Exit(1)
	}

	switch result.Action {
	case updater.ActionNoChange:
		logger.Info("done - no change (ip=%s)", result.PublicIP)
	case updater.ActionUpdated:
		if result.DryRun {
			logger.Info("done — [dry-run] would update to ip=%s", result.PublicIP)
		} else {
			logger.Info("done — updated dns record to ip=%s", result.PublicIP)
		}
	case updater.ActionCreated:
		if result.DryRun {
			logger.Info("done — [dry-run] would create record with ip=%s", result.PublicIP)
		} else {
			logger.Info("done — created dns record with ip=%s", result.PublicIP)
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

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	u := updater.New(cfg, sm)
	if err := u.Test(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
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

	dir := filepath.Dir(resolvedPath)
	sm := state.New(state.DefaultStatePath(dir))
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

	if st != nil {
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

	if cfg.APIToken == "" {
		fmt.Print("Hetzner API Token: ")
		_, _ = fmt.Scanln(&cfg.APIToken)
	} else {
		fmt.Printf("Hetzner API Token (current: %s...): ", cfg.APIToken[:5])
		var newToken string
		_, _ = fmt.Scanln(&newToken)
		if newToken != "" {
			cfg.APIToken = newToken
		}
	}

	dnsClient := dns.New(cfg.APIToken)
	ctx := context.Background()

	fmt.Print("Target Domain (e.g. home.example.com): ")
	_, _ = fmt.Scanln(&cfg.Record)

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

	zone, err := dnsClient.FindZoneByRecord(ctx, cfg.Record)
	if err != nil {
		zone, err = dnsClient.FindZone(ctx, cfg.Zone)
	}

	if err != nil {
		fmt.Printf("Zone %q not found. Do you want to create it? (y/n): ", cfg.Zone)
		var confirm string
		_, _ = fmt.Scanln(&confirm)
		if strings.ToLower(confirm) == "y" {
			if !strings.Contains(cfg.Zone, ".") {
				fmt.Fprintf(os.Stderr, "Error: %q does not look like a valid zone name (must contain at least one dot).\n", cfg.Zone)
				os.Exit(1)
			}
			var err2 error
			zone, err2 = dnsClient.CreateZone(ctx, cfg.Zone, cfg.TTL)
			if err2 != nil {
				fmt.Fprintf(os.Stderr, "Error creating zone: %v\n", err2)
				os.Exit(1)
			}
			fmt.Printf("Zone %q created successfully (ID: %d).\n", zone.Name, zone.ID)
		} else {
			fmt.Println("Setup cancelled.")
			os.Exit(1)
		}
	}

	if zone == nil {
		fmt.Fprintln(os.Stderr, "Error: No zone resolved.")
		os.Exit(1)
	}

	cfg.Zone = zone.Name
	cfg.ZoneID = fmt.Sprintf("%d", zone.ID)
	fmt.Printf("Using Zone: %s (ID: %s)\n", zone.Name, cfg.ZoneID)

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
