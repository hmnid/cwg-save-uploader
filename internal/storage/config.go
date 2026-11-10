package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type Config struct {
	SaveFolder    string `json:"save_folder"`
	PollSeconds   int    `json:"poll_interval_seconds"`
	Automatic     bool   `json:"upload_automatically"`
	LaunchAtLogin bool   `json:"launch_at_login"`
}

func LoadConfig(dir, defaultFolder string) (Config, bool, error) {
	config := Config{SaveFolder: defaultFolder, PollSeconds: 5, Automatic: true}
	data, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if errors.Is(err, os.ErrNotExist) {
		return config, true, SaveConfig(dir, config)
	}
	if err != nil {
		return config, false, err
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return config, false, fmt.Errorf("invalid config.json: %w", err)
	}
	if config.PollSeconds < 1 || config.PollSeconds > 3600 {
		return config, false, errors.New("poll_interval_seconds must be between 1 and 3600")
	}
	expanded, err := ExpandPath(config.SaveFolder)
	if err != nil {
		return config, false, err
	}
	if !filepath.IsAbs(expanded) {
		return config, false, errors.New("save_folder must be an absolute path")
	}
	return config, false, nil
}

func SaveConfig(dir string, config Config) error {
	return atomicJSON(filepath.Join(dir, "config.json"), config)
}

var variable = regexp.MustCompile(`%([^%]+)%`)

func ExpandPath(path string) (string, error) {
	var missing string
	expanded := variable.ReplaceAllStringFunc(path, func(match string) string {
		name := strings.Trim(match, "%")
		value, exists := os.LookupEnv(name)
		if !exists {
			missing = name
		}
		return value
	})
	if missing != "" {
		return "", fmt.Errorf("environment variable %s is not set", missing)
	}
	return filepath.Clean(expanded), nil
}

func atomicJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".write-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(append(data, '\n')); err != nil {
		file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
