package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

const (
	appDirName = "i2p-vanitygen"
	configFile = "config.json"
)

// Config holds persistent user preferences.
type Config struct {
	TelemetryAsked   bool   `json:"telemetry_asked"`
	TelemetryOptedIn bool   `json:"telemetry_opted_in"`
	SkippedVersion   string `json:"skipped_version,omitempty"`
}

// Load reads the config from disk. If the file does not exist,
// it returns a zero-value Config (TelemetryAsked == false).
func Load() *Config {
	p, err := path()
	if err != nil {
		return &Config{}
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &Config{}
		}
		return &Config{}
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return &Config{}
	}
	return &cfg
}

// Save writes the config to disk, creating the directory if needed.
func Save(cfg *Config) error {
	p, err := path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0600)
}

func path() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, appDirName, configFile), nil
}
