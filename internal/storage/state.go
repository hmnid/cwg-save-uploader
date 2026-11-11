package storage

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

type UploadState struct {
	SaveFolder  string    `json:"save_folder"`
	Destination string    `json:"destination"`
	Fingerprint string    `json:"fingerprint"`
	UploadedAt  time.Time `json:"uploaded_at"`
	Player      string    `json:"player"`
	Heroes      int       `json:"heroes"`
}

func LoadUploadState(dir string) (UploadState, error) {
	var state UploadState
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

func SaveUploadState(dir string, state UploadState) error {
	return atomicJSON(filepath.Join(dir, "state.json"), state)
}
