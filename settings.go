package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

const (
	defaultListenSeconds = 180
	defaultPPS           = 80
	minListenSeconds     = 30
	maxListenSeconds     = 900
	minPPS               = 1
	maxPPS               = 500
)

// settings holds the tunable probe parameters the panel exposes, plus this
// server's own identity (source "ایران" vs exit "خارج", and a free label), so
// the operator can tell panels apart at a glance.
type settings struct {
	ListenSeconds int    `json:"listen_seconds"`
	PacketsPerSec int    `json:"packets_per_sec"`
	SiteRole      string `json:"site_role"` // "", "source" (ایران/مبدأ), or "exit" (خارج)
	SiteName      string `json:"site_name"` // free label, e.g. "ایران-۱" or "آلمان-۳"
}

type settingsStore struct {
	path string
	mu   sync.Mutex
	cur  settings
}

func defaultSettings() settings {
	return settings{ListenSeconds: defaultListenSeconds, PacketsPerSec: defaultPPS}
}

// clamp keeps the values inside the supported range, filling zeros with defaults.
func (s settings) clamp() settings {
	if s.ListenSeconds == 0 {
		s.ListenSeconds = defaultListenSeconds
	}
	if s.PacketsPerSec == 0 {
		s.PacketsPerSec = defaultPPS
	}
	if s.ListenSeconds < minListenSeconds {
		s.ListenSeconds = minListenSeconds
	}
	if s.ListenSeconds > maxListenSeconds {
		s.ListenSeconds = maxListenSeconds
	}
	if s.PacketsPerSec < minPPS {
		s.PacketsPerSec = minPPS
	}
	if s.PacketsPerSec > maxPPS {
		s.PacketsPerSec = maxPPS
	}
	if s.SiteRole != "source" && s.SiteRole != "exit" {
		s.SiteRole = ""
	}
	if len(s.SiteName) > 60 {
		s.SiteName = s.SiteName[:60]
	}
	return s
}

func openSettings(dir string) (*settingsStore, error) {
	path := filepath.Join(dir, "settings.json")
	st := &settingsStore{path: path, cur: defaultSettings()}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return st, nil
		}
		return nil, err
	}
	var s settings
	if err := json.Unmarshal(raw, &s); err != nil {
		// A corrupt file should not keep the panel from starting.
		return st, nil
	}
	st.cur = s.clamp()
	return st, nil
}

func (st *settingsStore) get() settings {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.cur
}

// set validates, persists atomically, and returns the stored value.
func (st *settingsStore) set(n settings) (settings, error) {
	v := n.clamp()
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return settings{}, err
	}
	tmp := st.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0600); err != nil {
		return settings{}, err
	}
	if err := os.Rename(tmp, st.path); err != nil {
		return settings{}, err
	}
	st.mu.Lock()
	st.cur = v
	st.mu.Unlock()
	return v, nil
}
