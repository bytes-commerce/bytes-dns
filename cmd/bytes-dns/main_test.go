package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bytes-commerce/bytes-dns/internal/installer"
)

func TestCmdInstall_RequiresRoot(t *testing.T) {
	inst := installer.New(installer.Options{
		SourceBinary: "/nonexistent",
		BinaryPath:   "/tmp/bytes-dns-test",
		SystemdDir:   "/tmp",
		ConfigDir:    filepath.Join(t.TempDir(), ".bytes-dns"),
		User:         "testuser",
		IntervalMins: 5,
	})
	inst.RootCheck = func() error { return installer.ErrNotRoot }

	err := inst.Install(context.Background())
	if err == nil {
		t.Fatal("expected error when not root, got nil")
	}
	if !strings.Contains(errString(err), "root") {
		t.Errorf("expected root error message, got: %v", errString(err))
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
