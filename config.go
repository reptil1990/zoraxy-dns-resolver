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
	DNSPort       int      `json:"dns_port"`
	Upstreams     []string `json:"upstreams"`
	ZoraxyLANIP   string   `json:"zoraxy_lan_ip"`
	AutoSync      bool     `json:"auto_sync"`
	StaticRecords []Record `json:"static_records"`
}

func defaultConfig() Config {
	return Config{
		DNSPort:       53,
		Upstreams:     []string{"1.1.1.1:53"},
		ZoraxyLANIP:   "",
		AutoSync:      true,
		StaticRecords: []Record{},
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

func (s *configStore) save() error {
	s.mu.RLock()
	data, err := json.MarshalIndent(s.cfg, "", "  ")
	s.mu.RUnlock()
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o644)
}
