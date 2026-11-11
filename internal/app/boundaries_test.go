package app

import (
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cwg2uploader/internal/storage"
)

func TestPollingErrorIsReportedAndLoggedWhileUploadIsActive(t *testing.T) {
	application, client := fixtureApp(t)
	var logs bytes.Buffer
	application.Logger = log.New(&logs, "", 0)
	state := newRunState(application)
	state.poller.scanner.Folder = filepath.Join(t.TempDir(), "missing")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer func() { cancel(); state.uploader.wait() }()

	state.uploader.queue(fixtureFolderUpdate("accepted", "save"))
	state.dispatchUploads(ctx, time.Now())
	request := nextRequest(t, ctx, client)
	state.pollFolder(ctx, time.Now())
	whileUploading := state.lastMessage
	observed := state.view.folder
	request.reply <- uploadReply{result: successfulResult()}
	state.uploader.handleUploadResult(nextUploadResult(t, ctx, state.uploader))

	if observed.kind != pollFailed || observed.err == nil {
		t.Fatal("upload activity hid the folder observation")
	}
	if !strings.Contains(logs.String(), "[poller] save scan failed:") || !strings.Contains(logs.String(), "[uploader] upload started:") {
		t.Fatalf("missing component logs: %s", logs.String())
	}
	if whileUploading != "Uploading saves" {
		t.Fatalf("active-upload status = %q", whileUploading)
	}
	if !strings.Contains(state.lastMessage, "read save folder") {
		t.Fatal("folder error was not visible after upload completion")
	}
	if application.LastUpload.Fingerprint != "accepted" {
		t.Fatal("folder error interfered with successful delivery")
	}
}

func TestStandaloneUploaderEmitsSuccessWithoutMutatingAppOrPersisting(t *testing.T) {
	application, client := fixtureApp(t)
	var events []uploadEvent
	uploader := newSnapshotUploader(client, false, componentLogger(application.Logger, "uploader"), func(event uploadEvent) { events = append(events, event) })
	update := fixtureFolderUpdate("delivered", "save")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer func() { cancel(); uploader.wait() }()

	uploader.queue(update)
	uploader.uploadPending(ctx, time.Now())
	request := nextRequest(t, ctx, client)
	request.reply <- uploadReply{result: successfulResult()}
	uploader.handleUploadResult(nextUploadResult(t, ctx, uploader))
	_, stateErr := os.Stat(filepath.Join(application.Dir, "state.json"))

	if application.LastUpload.Fingerprint != "" || !os.IsNotExist(stateErr) {
		t.Fatal("standalone uploader reached into app state or persistence")
	}
	if len(events) == 0 {
		t.Fatal("uploader did not emit events")
	}
	success := events[len(events)-1]
	if success.kind != uploadSucceeded || success.update.snapshot.Fingerprint != "delivered" || success.result.Heroes != 1 || success.at.IsZero() {
		t.Fatalf("success event = %+v", success)
	}
}

func TestCoordinatorPersistsSuccessfulUploadEvent(t *testing.T) {
	application, _ := fixtureApp(t)
	state := newRunState(application)
	uploadedAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	event := uploadEvent{kind: uploadSucceeded, update: fixtureFolderUpdate("delivered", "save"), result: successfulResult(), at: uploadedAt}

	state.handleUploadEvent(event)
	persisted, err := storage.LoadUploadState(application.Dir)

	if err != nil {
		t.Fatal(err)
	}
	if persisted.Fingerprint != "delivered" || persisted.SaveFolder != event.update.saveFolder || !persisted.UploadedAt.Equal(uploadedAt) {
		t.Fatalf("persisted state = %+v", persisted)
	}
	if application.LastUpload != persisted {
		t.Fatal("coordinator did not update app upload history")
	}
}

func TestCoordinatorReportsPersistenceFailureUsingItsOwnLogger(t *testing.T) {
	application, _ := fixtureApp(t)
	var logs bytes.Buffer
	application.Logger = log.New(&logs, "", 0)
	path := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	application.Dir = path
	state := newRunState(application)
	event := uploadEvent{kind: uploadSucceeded, update: fixtureFolderUpdate("delivered", "save"), result: successfulResult(), at: time.Now()}

	state.handleUploadEvent(event)

	if application.LastUpload.Fingerprint != "delivered" || state.view.persistenceError == nil {
		t.Fatal("successful delivery and persistence failure were conflated")
	}
	if !strings.Contains(state.lastMessage, "could not persist successful state") {
		t.Fatalf("status = %q", state.lastMessage)
	}
	if !strings.Contains(logs.String(), "[app] save upload state failed:") {
		t.Fatalf("missing coordinator log: %s", logs.String())
	}
}

func TestUploaderResetRejectsActiveWorkWithoutDiscardingPendingData(t *testing.T) {
	application, client := fixtureApp(t)
	var events []uploadEvent
	uploader := newSnapshotUploader(client, false, componentLogger(application.Logger, "uploader"), func(event uploadEvent) { events = append(events, event) })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer func() { cancel(); uploader.wait() }()

	uploader.queue(fixtureFolderUpdate("active", "first"))
	uploader.uploadPending(ctx, time.Now())
	_ = nextRequest(t, ctx, client)
	uploader.queue(fixtureFolderUpdate("pending", "second"))
	err := uploader.reset()

	if err == nil || !uploader.busy {
		t.Fatal("reset accepted an active upload")
	}
	if uploader.pending == nil || uploader.pending.snapshot.Fingerprint != "pending" {
		t.Fatal("failed reset discarded pending work")
	}
	if events[len(events)-1].kind != uploadStarted {
		t.Fatal("failed reset emitted a reset transition")
	}
}

func TestUploaderResetOwnsTheEntireIdleDeliveryTransition(t *testing.T) {
	application, _ := fixtureApp(t)
	var events []uploadEvent
	uploader := newSnapshotUploader(application.Client, false, componentLogger(application.Logger, "uploader"), func(event uploadEvent) { events = append(events, event) })
	uploader.queue(fixtureFolderUpdate("pending", "save"))
	uploader.rejection = &rejection{fingerprint: "old", message: "invalid save"}
	uploader.retryAt, uploader.attempt = time.Now().Add(time.Hour), 4

	err := uploader.reset()

	if err != nil {
		t.Fatal(err)
	}
	if uploader.pending != nil || uploader.rejection != nil || !uploader.retryAt.IsZero() || uploader.attempt != 0 {
		t.Fatal("reset retained delivery state")
	}
	if events[len(events)-1].kind != uploadIdle {
		t.Fatal("reset did not report its resulting state")
	}
}

func TestFailedFolderConfigWriteDoesNotResetUploader(t *testing.T) {
	application, _ := fixtureApp(t)
	state := newRunState(application)
	originalFolder := application.Config.SaveFolder
	replacement := t.TempDir()
	state.uploader.queue(fixtureFolderUpdate("pending", "save"))
	deadline := time.Now().Add(time.Hour)
	state.uploader.retryAt = deadline
	path := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	application.Dir = path

	err := state.handleCommand(context.Background(), Command{Kind: "folder", Value: replacement})

	if err == nil {
		t.Fatal("config write failure not reported")
	}
	if application.Config.SaveFolder != originalFolder || state.poller.saveFolder != originalFolder {
		t.Fatal("failed config write changed the selected folder")
	}
	if state.uploader.pending == nil || state.uploader.pending.snapshot.Fingerprint != "pending" || !state.uploader.retryAt.Equal(deadline) {
		t.Fatal("failed config write reset accepted work")
	}
}
