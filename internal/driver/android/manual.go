package android

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
)

// manualStore keeps the phones that ignore Pinky's change to their
// accessibility settings (companionLink.manual; vivo's OriginOS 6, GitHub
// issue #94) in a file, or in memory when it has no path. On such a phone
// only the person can turn Pinky on the phone's service on, and the driver
// leaves on a service they turned on; it must know the phone as it opens,
// before the first AgentWorking(false) would turn a service left on off
// (quietLocked), so the knowledge outlives Pinky's restarts. A nil store
// keeps nothing.
type manualStore struct {
	path string
	logf func(string, ...any)

	mu     sync.Mutex
	loaded bool
	phones []string // by the id the provider lists them by
}

type manualFile struct {
	Phones []string `json:"phones"`
}

func (s *manualStore) loadLocked() {
	if s.loaded {
		return
	}
	s.loaded = true
	if s.path == "" {
		return
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			s.logf("android: reading the phones that ignore Pinky's change to their accessibility settings: %v", err)
		}
		return
	}
	var f manualFile
	if err := json.Unmarshal(b, &f); err != nil {
		s.logf("android: reading %s: %v", s.path, err)
		return
	}
	for _, id := range f.Phones {
		if id != "" && !slices.Contains(s.phones, id) {
			s.phones = append(s.phones, id)
		}
	}
}

// has reports whether the phone is one that ignores Pinky's change.
func (s *manualStore) has(id string) bool {
	if s == nil || id == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	return slices.Contains(s.phones, id)
}

// set notes whether the phone ignores Pinky's change, and saves it.
func (s *manualStore) set(id string, manual bool) {
	if s == nil || id == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	i := slices.Index(s.phones, id)
	switch {
	case manual && i < 0:
		s.phones = append(s.phones, id)
	case !manual && i >= 0:
		s.phones = slices.Delete(s.phones, i, i+1)
	default:
		return
	}
	if err := s.saveLocked(); err != nil {
		s.logf("android: saving the phones that ignore Pinky's change to their accessibility settings: %v", err)
	}
}

func (s *manualStore) saveLocked() error {
	if s.path == "" {
		return nil
	}
	b, err := json.MarshalIndent(manualFile{Phones: s.phones}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
