package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type State struct {
	LastIP       string    `json:"last_ip"`
	LastRecordID string    `json:"last_record_id"`
	LastUpdated  time.Time `json:"last_updated"`
	LastChecked  time.Time `json:"last_checked"`
	LastSyncedIP string    `json:"last_synced_ip"`
}

type Manager struct {
	path string
}

func New(path string) *Manager {
	return &Manager{path: path}
}

func DefaultStatePath(configDir string) string {
	return filepath.Join(configDir, "state.json")
}

func (m *Manager) Load() (*State, error) {
	data, err := os.ReadFile(m.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &State{}, nil
		}
		return nil, fmt.Errorf("cannot read state file %s: %w", m.path, err)
	}

	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return &State{}, nil
	}
	return &s, nil
}

func (m *Manager) Save(s *State) error {
	if err := os.MkdirAll(filepath.Dir(m.path), 0o700); err != nil {
		return fmt.Errorf("cannot create state directory: %w", err)
	}

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("cannot marshal state: %w", err)
	}

	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("cannot write state temp file: %w", err)
	}

	if err := os.Rename(tmp, m.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("cannot commit state file: %w", err)
	}

	return nil
}

func (m *Manager) MarkChecked(s *State) error {
	s.LastChecked = time.Now().UTC()
	return m.Save(s)
}

func (m *Manager) MarkUpdated(s *State, ip, recordID string) error {
	s.LastIP = ip
	s.LastSyncedIP = ip
	s.LastRecordID = recordID
	s.LastUpdated = time.Now().UTC()
	s.LastChecked = s.LastUpdated
	return m.Save(s)
}
