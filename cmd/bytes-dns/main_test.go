package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bytes-commerce/bytes-dns/internal/installer"
	"github.com/bytes-commerce/bytes-dns/internal/selfupdate"
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

func TestCmdUpdate_RequiresRoot(t *testing.T) {
	inst := selfupdate.New(selfupdate.Options{
		RepoOwner: "bytes-commerce",
		RepoName:  "bytes-dns",
		Version:   "1.0.0",
		BinaryDir: t.TempDir(),
		User:      "alice",
	})
	wantErr := errors.New("installer must be run as root")
	inst.RootCheck = func() error { return wantErr }

	if err := inst.RootCheck(); !errors.Is(err, wantErr) {
		t.Fatalf("RootCheck() error = %v, want %v", err, wantErr)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
