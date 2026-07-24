// Package installer handles installation of the bytes-dns binary and
// systemd units on Linux systems.
package installer

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

//go:embed assets/bytes-dns.service
var serviceTemplate string

//go:embed assets/bytes-dns.timer
var timerTemplate string

var errNotRoot = errors.New("installer must be run as root")

// Installer performs the bytes-dns install or uninstall on a Linux system.
// All file paths and external commands are overridable for testing.
type Installer struct {
	// BinaryPath is the destination for the bytes-dns binary (e.g. /usr/local/bin/bytes-dns).
	BinaryPath string
	// SystemdDir is where unit files are written (e.g. /etc/systemd/system).
	SystemdDir string
	// ConfigDir is the user's bytes-dns config directory (e.g. ~/.bytes-dns).
	ConfigDir string
	// User is the user the timer will run as (template instance %i).
	User string
	// IntervalMins is the timer interval in minutes.
	IntervalMins int

	// Hooks for testing. Defaults call the real OS.
	RootCheck    func() error
	copySelf     func(dst string) error
	runSystemctl func(ctx context.Context, name string, args ...string) (string, error)
}

// New returns an Installer configured for the current system.
// Caller must ensure BinPath is the path to the running binary.
func New(opts Options) *Installer {
	return &Installer{
		BinaryPath:   opts.BinaryPath,
		SystemdDir:   opts.SystemdDir,
		ConfigDir:    opts.ConfigDir,
		User:         opts.User,
		IntervalMins: opts.IntervalMins,
		RootCheck:    defaultRootCheck,
		copySelf:     func(dst string) error { return copyFile(opts.SourceBinary, dst) },
		runSystemctl: defaultRunSystemctl,
	}
}

// Options configures a new Installer.
type Options struct {
	SourceBinary string
	BinaryPath   string
	SystemdDir   string
	ConfigDir    string
	User         string
	IntervalMins int
}

// Install performs the full install: root check, binary copy, unit files,
// daemon-reload, enable --now, and verification.
func (i *Installer) Install(ctx context.Context) error {
	if i.RootCheck != nil {
		if err := i.RootCheck(); err != nil {
			return err
		}
	}

	if err := os.MkdirAll(filepath.Dir(i.BinaryPath), 0o755); err != nil {
		return fmt.Errorf("create binary dir: %w", err)
	}
	if err := i.copySelf(i.BinaryPath); err != nil {
		return fmt.Errorf("copy binary to %s: %w", i.BinaryPath, err)
	}
	if err := os.Chmod(i.BinaryPath, 0o755); err != nil {
		return fmt.Errorf("chmod binary: %w", err)
	}

	if err := i.writeUnitFiles(); err != nil {
		return err
	}

	if _, err := i.runSystemctl(ctx, "daemon-reload"); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %w", err)
	}

	timerUnit := fmt.Sprintf("bytes-dns@%s.timer", i.User)
	if _, err := i.runSystemctl(ctx, "enable", "--now", timerUnit); err != nil {
		return fmt.Errorf("systemctl enable --now %s: %w", timerUnit, err)
	}

	// Verify the timer is active.
	if _, err := i.runSystemctl(ctx, "is-active", timerUnit); err != nil {
		return fmt.Errorf("timer %s is not active after install: %w", timerUnit, err)
	}

	return nil
}

// Uninstall removes the systemd units and disables the timer.
func (i *Installer) Uninstall(ctx context.Context) error {
	if i.RootCheck != nil {
		if err := i.RootCheck(); err != nil {
			return err
		}
	}

	timerUnit := fmt.Sprintf("bytes-dns@%s.timer", i.User)
	svcUnit := fmt.Sprintf("bytes-dns@%s.service", i.User)

	for _, u := range []string{timerUnit, svcUnit} {
		_, _ = i.runSystemctl(ctx, "disable", "--now", u)
	}

	for _, f := range []string{
		filepath.Join(i.SystemdDir, "bytes-dns@.service"),
		filepath.Join(i.SystemdDir, "bytes-dns@.timer"),
	} {
		_ = os.Remove(f)
	}

	_, _ = i.runSystemctl(ctx, "daemon-reload")

	_ = os.Remove(i.BinaryPath)
	return nil
}

func (i *Installer) writeUnitFiles() error {
	if err := os.MkdirAll(i.SystemdDir, 0o755); err != nil {
		return fmt.Errorf("create systemd dir: %w", err)
	}

	svc := serviceTemplate
	if err := os.WriteFile(filepath.Join(i.SystemdDir, "bytes-dns@.service"), []byte(svc), 0o644); err != nil {
		return fmt.Errorf("write service unit: %w", err)
	}

	timer := renderTimer([]byte(timerTemplate), i.IntervalMins)
	if err := os.WriteFile(filepath.Join(i.SystemdDir, "bytes-dns@.timer"), []byte(timer), 0o644); err != nil {
		return fmt.Errorf("write timer unit: %w", err)
	}
	return nil
}

// renderTimer substitutes the interval placeholder in the timer template.
func renderTimer(template []byte, intervalMins int) string {
	return strings.ReplaceAll(string(template), "INTERVAL_PLACEHOLDER", fmt.Sprintf("%dmin", intervalMins))
}

func defaultRootCheck() error {
	if os.Getuid() != 0 {
		return errNotRoot
	}
	return nil
}

func defaultRunSystemctl(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func copyFile(src, dst string) error {
	in, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, in, 0o755)
}
