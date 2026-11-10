package storage

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

type State struct {
	Folder      string    `json:"folder"`
	Destination string    `json:"destination"`
	Fingerprint string    `json:"fingerprint"`
	UploadedAt  time.Time `json:"uploaded_at"`
	Player      string    `json:"player"`
	Heroes      int       `json:"heroes"`
}

func LoadState(dir string) (State, error) {
	var state State
	data, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	err = json.Unmarshal(data, &state)
	return state, err
}

func SaveState(dir string, state State) error {
	return atomicJSON(filepath.Join(dir, "state.json"), state)
}
