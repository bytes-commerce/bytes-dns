package installer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstaller_InstallWritesUnitsAndRecordsSystemctlCalls(t *testing.T) {
	tmp := t.TempDir()
	var calls [][]string
	run := func(ctx context.Context, name string, args ...string) (string, error) {
		calls = append(calls, append([]string{name}, args...))
		return "", nil
	}

	inst := &Installer{
		BinaryPath:   filepath.Join(tmp, "bin", "bytes-dns"),
		SystemdDir:   filepath.Join(tmp, "systemd"),
		ConfigDir:    filepath.Join(tmp, "config"),
		User:         "alice",
		IntervalMins: 5,
		runSystemctl: run,
		copySelf:     func(dst string) error { return os.WriteFile(dst, []byte("test"), 0o755) },
	}

	if err := inst.Install(context.Background()); err != nil {
		t.Fatalf("Install failed: %v", err)
	}

	// Verify unit files were written.
	svcPath := filepath.Join(tmp, "systemd", "bytes-dns@.service")
	timerPath := filepath.Join(tmp, "systemd", "bytes-dns@.timer")
	for _, p := range []string{svcPath, timerPath} {
		if _, err := readFile(p); err != nil {
			t.Errorf("expected %s to exist: %v", p, err)
		}
	}

	// Verify timer is templated with the user.
	svcBytes, _ := readFile(svcPath)
	if !strings.Contains(string(svcBytes), "User=%i") {
		t.Errorf("service file should template User=%%i; got: %s", string(svcBytes))
	}
	timerBytes, _ := readFile(timerPath)
	timerStr := string(timerBytes)

	if strings.Contains(timerStr, "INTERVAL_PLACEHOLDER") {
		t.Errorf("timer file should not contain literal INTERVAL_PLACEHOLDER; got: %s", timerStr)
	}
	if !strings.Contains(timerStr, "OnUnitActiveSec=5min") {
		t.Errorf("timer file should contain rendered OnUnitActiveSec=5min; got: %s", timerStr)
	}

	// Verify systemctl was called in the right order.
	if !containsArgs(calls, "daemon-reload") {
		t.Errorf("expected daemon-reload call, got: %v", calls)
	}
	if !containsArgs(calls, "enable", "--now") {
		t.Errorf("expected enable --now call, got: %v", calls)
	}
}

func TestInstaller_InstallReturnsErrorWhenNotRoot(t *testing.T) {
	inst := &Installer{
		RootCheck: func() error { return ErrNotRoot },
	}
	if err := inst.Install(context.Background()); err == nil {
		t.Fatal("expected error when not root, got nil")
	}
}

func containsArgs(calls [][]string, want ...string) bool {
	for _, c := range calls {
		match := true
		for i, w := range want {
			if i >= len(c) || c[i] != w {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func readFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}
