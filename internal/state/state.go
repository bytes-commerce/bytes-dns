package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// legacyProvider is the provider the flat state fields belong to. They are
// kept for backwards compatibility with state files written before the
// provider aware state was introduced.
const legacyProvider = "hetzner"

type State struct {
	// The flat fields mirror the state of the primary provider.
	LastIP       string    `json:"last_ip"`
	LastRecordID string    `json:"last_record_id"`
	LastUpdated  time.Time `json:"last_updated"`
	LastChecked  time.Time `json:"last_checked"`

	// Providers holds the per provider state. Every provider keeps its own
	// last known IP so that one provider never suppresses the update of
	// another.
	Providers map[string]ProviderState `json:"providers,omitempty"`
}

// ProviderState is the state of a single provider.
type ProviderState struct {
	LastIP       string    `json:"last_ip"`
	LastRecordID string    `json:"last_record_id"`
	LastUpdated  time.Time `json:"last_updated"`
	// LastError holds the message of the last failed run of the provider.
	// It is cleared as soon as a run of the provider completes without an
	// error.
	LastError   string     `json:"last_error,omitempty"`
	LastErrorAt *time.Time `json:"last_error_at,omitempty"`
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

// Load reads the state file. If the file is missing, returns an empty state.
// If the file is corrupt, renames it to state.json.broken.<unix-timestamp>
// and returns an empty state.
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
		// Quarantine the corrupt file so we can debug it later.
		brokenPath := fmt.Sprintf("%s.broken.%d", m.path, time.Now().Unix())
		if renameErr := os.Rename(m.path, brokenPath); renameErr != nil {
			// If rename fails, fall back to empty state but log nothing here
			// (the logger package would create a circular dep).
			return &State{}, nil
		}
		return &State{}, nil
	}

	s.migrateLegacy()

	return &s, nil
}

// migrateLegacy moves a flat state file into the provider map so an
// existing installation keeps its "IP unchanged" shortcut after the update.
func (s *State) migrateLegacy() {
	if len(s.Providers) > 0 || s.LastIP == "" {
		return
	}

	s.Providers = map[string]ProviderState{
		legacyProvider: {
			LastIP:       s.LastIP,
			LastRecordID: s.LastRecordID,
			LastUpdated:  s.LastUpdated,
		},
	}
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
	return m.MarkCheckedFor(s, legacyProvider)
}

// MarkCheckedFor records that the provider was checked without any change.
func (m *Manager) MarkCheckedFor(s *State, provider string) error {
	s.LastChecked = time.Now().UTC()

	s.ensureProviders()
	current := s.Providers[provider]
	s.Providers[provider] = ProviderState{
		LastIP:       current.LastIP,
		LastRecordID: current.LastRecordID,
		LastUpdated:  current.LastUpdated,
	}

	return m.Save(s)
}

func (m *Manager) MarkUpdated(s *State, ip, recordID string) error {
	return m.MarkUpdatedFor(s, legacyProvider, ip, recordID)
}

// MarkUpdatedFor records a successful update of the given provider.
func (m *Manager) MarkUpdatedFor(s *State, provider, ip, recordID string) error {
	now := time.Now().UTC()

	// The flat fields always mirror the primary provider. Writing them for
	// any other provider would make the legacy fallback of LastIPFor report
	// a foreign address for the primary provider.
	if provider == legacyProvider {
		s.LastIP = ip
		s.LastRecordID = recordID
		s.LastUpdated = now
	}
	s.LastChecked = now

	s.ensureProviders()
	s.Providers[provider] = ProviderState{
		LastIP:       ip,
		LastRecordID: recordID,
		LastUpdated:  now,
	}

	return m.Save(s)
}

// MarkErrorFor records a failed run of the given provider. The last known IP
// stays untouched so the next run still performs the update.
func (m *Manager) MarkErrorFor(s *State, provider string, cause error) error {
	if cause == nil {
		return nil
	}

	now := time.Now().UTC()

	s.ensureProviders()
	current := s.Providers[provider]
	current.LastError = cause.Error()
	at := now
	current.LastErrorAt = &at
	s.Providers[provider] = current

	return m.Save(s)
}

// LastErrorFor returns the message of the last failed run of the given
// provider.
func (m *Manager) LastErrorFor(s *State, provider string) string {
	if s == nil {
		return ""
	}
	return s.Providers[provider].LastError
}

// LastIPFor returns the last known IP of the given provider.
func (m *Manager) LastIPFor(s *State, provider string) string {
	if s == nil {
		return ""
	}
	if st, ok := s.Providers[provider]; ok {
		return st.LastIP
	}
	if provider == legacyProvider {
		return s.LastIP
	}
	return ""
}

// LastUpdatedFor returns the time of the last successful update of the
// given provider.
func (m *Manager) LastUpdatedFor(s *State, provider string) time.Time {
	if s == nil {
		return time.Time{}
	}
	if st, ok := s.Providers[provider]; ok {
		return st.LastUpdated
	}
	if provider == legacyProvider {
		return s.LastUpdated
	}
	return time.Time{}
}

// LastRecordIDFor returns the record ID of the last update of the given
// provider.
func (m *Manager) LastRecordIDFor(s *State, provider string) string {
	if s == nil {
		return ""
	}
	if st, ok := s.Providers[provider]; ok {
		return st.LastRecordID
	}
	if provider == legacyProvider {
		return s.LastRecordID
	}
	return ""
}

func (s *State) ensureProviders() {
	if s.Providers == nil {
		s.Providers = map[string]ProviderState{}
	}
}
