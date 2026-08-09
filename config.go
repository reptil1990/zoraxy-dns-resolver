package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Record is a static internal DNS entry mapping a hostname to an IP.
type Record struct {
	Host string `json:"host"`
	IP   string `json:"ip"`
}

// Config holds the persisted plugin settings. It is stored as config.json
// next to the plugin binary and edited through the UI.
type Config struct {
	DNSPort          int      `json:"dns_port"`
	Upstreams        []string `json:"upstreams"`
	ZoraxyLANIP      string   `json:"zoraxy_lan_ip"`
	AutoSync         bool     `json:"auto_sync"`
	AutoSyncDisabled []string `json:"auto_sync_disabled"` // synced hosts the user turned off
	StaticRecords    []Record `json:"static_records"`
}

func defaultConfig() Config {
	return Config{
		DNSPort:          53,
		Upstreams:        []string{"1.1.1.1:53"},
		ZoraxyLANIP:      "",
		AutoSync:         true,
		AutoSyncDisabled: []string{},
		StaticRecords:    []Record{},
	}
}

type configStore struct {
	mu   sync.RWMutex
	cfg  Config
	path string
}

func configPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "config.json"
	}
	return filepath.Join(filepath.Dir(exe), "config.json")
}

// loadConfig reads config.json, writing defaults on first run.
func loadConfig() *configStore {
	s := &configStore{path: configPath()}
	data, err := os.ReadFile(s.path)
	if err != nil {
		s.cfg = defaultConfig()
		s.save()
		return s
	}
	cfg := defaultConfig()
	if err := json.Unmarshal(data, &cfg); err != nil {
		cfg = defaultConfig()
	}
	s.cfg = cfg
	return s
}

func (s *configStore) get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

func (s *configStore) set(cfg Config) error {
	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()
	return s.save()
}

// setHostEnabled adds or removes a host from the auto-sync disabled list.
func (s *configStore) setHostEnabled(host string, enabled bool) error {
	s.mu.Lock()
	kept := s.cfg.AutoSyncDisabled[:0:0]
	for _, h := range s.cfg.AutoSyncDisabled {
		if h != host {
			kept = append(kept, h)
		}
	}
	if !enabled {
		kept = append(kept, host)
	}
	s.cfg.AutoSyncDisabled = kept
	s.mu.Unlock()
	return s.save()
}

func (s *configStore) save() error {
	s.mu.RLock()
	data, err := json.MarshalIndent(s.cfg, "", "  ")
	s.mu.RUnlock()
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o644)
}
