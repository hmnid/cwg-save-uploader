package app

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"cwg2uploader/internal/saves"
	"cwg2uploader/internal/storage"
)

func TestCommandPausePersistsPreference(t *testing.T) {
	state := fixtureRunState(t)
	state.app.Config.ProxyURL = "http://proxy.invalid:8080"

	commandErr := state.handleCommand(context.Background(), Command{Kind: "pause"})
	config, _, loadErr := storage.LoadConfig(state.app.Dir, "unused")

	if commandErr != nil || loadErr != nil {
		t.Fatalf("errors: %v, %v", commandErr, loadErr)
	}
	if state.lastMessage != "Automatic uploads off" {
		t.Fatal("turning off automatic uploads must immediately update idle status")
	}
	if config.Automatic || state.app.Config.Automatic {
		t.Fatal("pause did not disable automatic uploads")
	}
	if config.ProxyURL != "http://proxy.invalid:8080" {
		t.Fatal("unrelated setting lost")
	}
}

func TestCommandResumeStartsCheckingStableSaves(t *testing.T) {
	state := fixtureRunState(t)
	state.app.Config.Automatic = false

	err := state.handleCommand(context.Background(), Command{Kind: "pause"})

	if err != nil {
		t.Fatal(err)
	}
	if !state.app.Config.Automatic || state.lastMessage != saves.ErrSettling.Error() {
		t.Fatal("resume must immediately start checking save stability")
	}
}

func TestCommandPauseKeepsPreferenceWhenPersistenceFails(t *testing.T) {
	state := fixtureRunState(t)
	path := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	state.app.Dir = path

	err := state.handleCommand(context.Background(), Command{Kind: "pause"})

	if err == nil {
		t.Fatal("persistence failure not reported")
	}
	if !state.app.Config.Automatic {
		t.Fatal("preference changed despite failed persistence")
	}
}

func TestCommandFolderRejectsChangeDuringUpload(t *testing.T) {
	state := fixtureRunState(t)
	original := state.app.Config.SaveFolder
	replacement := t.TempDir()
	state.uploader.busy = true

	err := state.handleCommand(context.Background(), Command{Kind: "folder", Value: replacement})

	if err == nil {
		t.Fatal("folder changed during an upload")
	}
	if state.app.Config.SaveFolder != original || state.poller.saveFolder != original {
		t.Fatal("folder was changed despite rejection")
	}
}

func TestCommandFolderPersistsAndWaitsForStableSaves(t *testing.T) {
	state := fixtureRunState(t)
	replacement := t.TempDir()
	state.uploader.rejection = &rejection{fingerprint: "rejected", message: "invalid save"}
	state.uploader.retryAt = time.Now().Add(time.Hour)
	state.uploader.attempt = 3
	state.app.ForceUploadRequested = true
	state.poller.forceNextEmission = true

	commandErr := state.handleCommand(context.Background(), Command{Kind: "folder", Value: replacement})
	config, _, loadErr := storage.LoadConfig(state.app.Dir, "unused")
	_, scanErr := state.poller.scanner.Scan(time.Now())

	if commandErr != nil || loadErr != nil {
		t.Fatalf("errors: %v, %v", commandErr, loadErr)
	}
	if state.lastMessage != saves.ErrSettling.Error() {
		t.Fatal("changing folders must start checking save stability immediately")
	}
	if config.SaveFolder != replacement || state.poller.saveFolder != replacement {
		t.Fatal("folder was not updated")
	}
	if scanErr != saves.ErrSettling {
		t.Fatalf("new folder must settle: %v", scanErr)
	}
	if state.uploader.rejection != nil || !state.uploader.retryAt.IsZero() || state.uploader.attempt != 0 {
		t.Fatal("old folder retry state retained")
	}
	if state.app.ForceUploadRequested || state.poller.forceNextEmission {
		t.Fatal("old folder upload request retained")
	}
}

func TestCommandStartupPersistsRegistration(t *testing.T) {
	state := fixtureRunState(t)
	var registrations []bool
	state.app.SetStartup = func(enabled bool) error { registrations = append(registrations, enabled); return nil }

	state.lastMessage = "Up to date"

	commandErr := state.handleCommand(context.Background(), Command{Kind: "startup"})
	config, _, loadErr := storage.LoadConfig(state.app.Dir, "unused")

	if commandErr != nil || loadErr != nil {
		t.Fatalf("errors: %v, %v", commandErr, loadErr)
	}
	select {
	case status := <-state.app.Updates:
		if status.Message != "Up to date" || !status.Config.LaunchAtLogin {
			t.Fatalf("startup change did not refresh existing status: %+v", status)
		}
	default:
		t.Fatal("startup preference change did not publish updated status")
	}
	if !config.LaunchAtLogin || !state.app.Config.LaunchAtLogin {
		t.Fatal("startup preference not persisted")
	}
	if !reflect.DeepEqual(registrations, []bool{true}) {
		t.Fatalf("registrations = %v", registrations)
	}
}

func TestCommandStartupRollsBackWhenPersistenceFails(t *testing.T) {
	state := fixtureRunState(t)
	path := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	state.app.Dir = path
	var registrations []bool
	state.app.SetStartup = func(enabled bool) error { registrations = append(registrations, enabled); return nil }

	err := state.handleCommand(context.Background(), Command{Kind: "startup"})

	if err == nil {
		t.Fatal("persistence failure not reported")
	}
	if state.app.Config.LaunchAtLogin {
		t.Fatal("preference changed despite failed persistence")
	}
	if !reflect.DeepEqual(registrations, []bool{true, false}) {
		t.Fatalf("registration not rolled back: %v", registrations)
	}
}

func TestCommandManualUploadKeepsRetryDeadline(t *testing.T) {
	state := fixtureRunState(t)
	deadline := time.Now().Add(time.Hour)
	state.uploader.retryAt = deadline
	state.uploader.rejection = &rejection{fingerprint: "rejected", message: "invalid save"}

	err := state.handleCommand(context.Background(), Command{Kind: "upload"})

	if err != nil {
		t.Fatal(err)
	}
	if state.lastMessage != saves.ErrSettling.Error() {
		t.Fatal("manual upload must immediately check save stability")
	}
	if state.app.ForceUploadRequested || !state.poller.forceNextEmission {
		t.Fatal("manual retry was not requested")
	}
	if state.uploader.rejection == nil || state.uploader.rejection.fingerprint != "rejected" {
		t.Fatal("upload request changed delivery state before emitting a snapshot")
	}
	if !state.uploader.retryAt.Equal(deadline) {
		t.Fatal("manual retry bypassed server retry deadline")
	}
}

func TestCommandUnknownDoesNotPoll(t *testing.T) {
	state := fixtureRunState(t)
	state.lastMessage = "Up to date"

	err := state.handleCommand(context.Background(), Command{Kind: "unknown"})

	if err != nil {
		t.Fatal(err)
	}
	if state.lastMessage != "Up to date" || len(state.app.Updates) != 0 {
		t.Fatal("unknown command must not poll or publish status")
	}
}

func TestPublishReplacesUnreadStatus(t *testing.T) {
	state := fixtureRunState(t)

	state.publish("first")
	state.publish("second")
	status := <-state.app.Updates

	if status.Message != "second" {
		t.Fatalf("status = %q", status.Message)
	}
}

func fixtureRunState(t *testing.T) *runState {
	t.Helper()
	application, _ := fixtureApp(t)
	return newRunState(application)
}
