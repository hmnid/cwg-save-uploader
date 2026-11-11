package storage

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestLoadConfigCreatesDefaults(t *testing.T) {
	dir := t.TempDir()
	folder := t.TempDir()

	config, firstRun, err := LoadConfig(dir, folder)

	if err != nil {
		t.Fatal(err)
	}
	if !firstRun || !config.Automatic || config.LaunchAtLogin || config.PollSeconds != 5 || config.SaveFolder != folder || config.ProxyURL != "" {
		t.Fatalf("unexpected defaults: %+v", config)
	}
}

func TestConfigPersistsUserSettings(t *testing.T) {
	dir := t.TempDir()
	want := Config{SaveFolder: t.TempDir(), PollSeconds: 10, Automatic: false, LaunchAtLogin: true, ProxyURL: "socks5://127.0.0.1:1080"}

	saveErr := SaveConfig(dir, want)
	got, firstRun, loadErr := LoadConfig(dir, "unused")

	if saveErr != nil || loadErr != nil {
		t.Fatalf("errors: %v, %v", saveErr, loadErr)
	}
	if firstRun || got != want {
		t.Fatalf("got %+v; want %+v", got, want)
	}
}

func TestConfigRejectsInvalidPollInterval(t *testing.T) {
	dir := t.TempDir()
	if err := SaveConfig(dir, Config{SaveFolder: t.TempDir(), PollSeconds: 0}); err != nil {
		t.Fatal(err)
	}

	_, _, err := LoadConfig(dir, "unused")

	if err == nil {
		t.Fatal("invalid interval accepted")
	}
}

func TestExpandPathExpandsWindowsVariables(t *testing.T) {
	folder := t.TempDir()
	t.Setenv("CWG2_TEST_HOME", folder)

	got, err := ExpandPath(`%CWG2_TEST_HOME%\saves`)

	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(folder, "saves") {
		t.Fatalf("got %q", got)
	}
}

func TestUploadStateAtomicallyReplacesPreviousState(t *testing.T) {
	dir := t.TempDir()
	first := UploadState{Fingerprint: "old"}
	want := UploadState{SaveFolder: "folder", Destination: "server", Fingerprint: "new", UploadedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), Player: "Player", Heroes: 2}

	firstErr := SaveUploadState(dir, first)
	secondErr := SaveUploadState(dir, want)
	got, loadErr := LoadUploadState(dir)

	if firstErr != nil || secondErr != nil || loadErr != nil {
		t.Fatalf("errors: %v, %v, %v", firstErr, secondErr, loadErr)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v; want %+v", got, want)
	}
}

func TestLoadUploadStateReadsSaveFolder(t *testing.T) {
	dir := t.TempDir()
	data := []byte(`{"save_folder":"saves","destination":"server","fingerprint":"existing","uploaded_at":"2025-01-01T00:00:00Z","player":"Player","heroes":2}`)
	if err := os.WriteFile(filepath.Join(dir, "state.json"), data, 0600); err != nil {
		t.Fatal(err)
	}

	want := UploadState{SaveFolder: "saves", Destination: "server", Fingerprint: "existing", UploadedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), Player: "Player", Heroes: 2}

	state, err := LoadUploadState(dir)

	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(state, want) {
		t.Fatalf("got %+v; want %+v", state, want)
	}
}

func TestLogRotatesWithoutExternalMaintenance(t *testing.T) {
	dir := t.TempDir()
	writer, err := OpenLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	writer.max = 4
	writer.backups = 2

	_, firstErr := writer.Write([]byte("1111"))
	_, secondErr := writer.Write([]byte("2222"))
	_, thirdErr := writer.Write([]byte("3333"))
	_, fourthErr := writer.Write([]byte("4444"))
	closeErr := writer.Close()
	current, readErr := os.ReadFile(filepath.Join(dir, "uploader.log"))
	previous, previousErr := os.ReadFile(filepath.Join(dir, "uploader.log.1"))
	oldest, oldestErr := os.ReadFile(filepath.Join(dir, "uploader.log.2"))
	_, extraErr := os.Stat(filepath.Join(dir, "uploader.log.3"))

	for _, err := range []error{firstErr, secondErr, thirdErr, fourthErr, closeErr, readErr, previousErr, oldestErr} {
		if err != nil {
			t.Fatal(err)
		}
	}
	if string(current) != "4444" || string(previous) != "3333" || string(oldest) != "2222" {
		t.Fatal("incorrect log rotation")
	}
	if !os.IsNotExist(extraErr) {
		t.Fatal("too many backups")
	}
}
