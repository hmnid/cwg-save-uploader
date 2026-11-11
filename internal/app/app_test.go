package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"cwg2uploader/internal/leaderboard"
	"cwg2uploader/internal/saves"
	"cwg2uploader/internal/storage"
)

func TestAppPersistsSuccessfulSnapshot(t *testing.T) {
	t.Parallel()
	application, client := fixtureApp(t)
	snapshot := readSnapshot(t, application.Config.SaveFolder)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	done := startApp(ctx, application)
	request := nextRequest(t, ctx, client)
	request.reply <- uploadReply{result: successfulResult()}
	status := awaitStatus(t, ctx, application, "successfully")
	cancel()
	<-done
	state, err := storage.LoadUploadState(application.Dir)

	if err != nil {
		t.Fatal(err)
	}
	if state.Fingerprint != snapshot.Fingerprint || state.Heroes != 1 || state.Player != "Player" {
		t.Fatalf("incorrect persisted state: %+v", state)
	}
	if status.LastUpload.IsZero() {
		t.Fatal("successful upload time not exposed")
	}
}

func TestAppSkipsSnapshotAlreadyUploadedBeforeRestart(t *testing.T) {
	t.Parallel()
	application, client := fixtureApp(t)
	scanner := saves.Scanner{Folder: application.Config.SaveFolder}
	_, _ = scanner.Scan(time.Now())
	snapshot, err := scanner.Scan(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	application.LastUpload = storage.UploadState{SaveFolder: application.Config.SaveFolder, Destination: leaderboard.BaseURL, Fingerprint: snapshot.Fingerprint}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	done := startApp(ctx, application)
	status := awaitStatus(t, ctx, application, "Up to date")
	cancel()
	<-done

	if status.Message != "Up to date" {
		t.Fatal(status.Message)
	}
	if len(client.requests) != 0 {
		t.Fatal("unchanged snapshot uploaded again")
	}
}

func TestAppExplicitUploadRetriesWithAutomaticUploadsOff(t *testing.T) {
	t.Parallel()
	application, client := fixtureApp(t)
	snapshot := readSnapshot(t, application.Config.SaveFolder)
	application.Config.Automatic = false
	application.LastUpload = storage.UploadState{SaveFolder: application.Config.SaveFolder, Destination: leaderboard.BaseURL, Fingerprint: snapshot.Fingerprint}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	done := startApp(ctx, application)
	_ = awaitStatus(t, ctx, application, "Automatic uploads off")
	application.Commands <- Command{Kind: "upload"}
	first := nextRequest(t, ctx, client)
	first.reply <- uploadReply{err: &leaderboard.UploadError{Message: "temporary failure", Retry: true}}
	second := nextRequest(t, ctx, client)
	second.reply <- uploadReply{result: successfulResult()}
	_ = awaitStatus(t, ctx, application, "successfully")
	cancel()
	<-done

	if application.Config.Automatic {
		t.Fatal("one-off upload enabled automatic uploads")
	}
	if application.LastUpload.UploadedAt.IsZero() || application.LastUpload.Fingerprint != snapshot.Fingerprint {
		t.Fatal("forced emission or its retry did not complete")
	}
	if !reflect.DeepEqual(first.payload, second.payload) {
		t.Fatal("retry changed the forced snapshot")
	}
}

func TestAppDoesNotPersistRejectedSnapshot(t *testing.T) {
	t.Parallel()
	application, client := fixtureApp(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	done := startApp(ctx, application)
	request := nextRequest(t, ctx, client)
	request.reply <- uploadReply{err: &leaderboard.UploadError{Message: "HTTP 400"}}
	_ = awaitStatus(t, ctx, application, "HTTP 400")
	cancel()
	<-done
	state, err := storage.LoadUploadState(application.Dir)

	if err != nil {
		t.Fatal(err)
	}
	if state.Fingerprint != "" {
		t.Fatal("rejected snapshot recorded as successful")
	}
}

func TestAppLogsQuotedErrorResponseBody(t *testing.T) {
	t.Parallel()
	application, client := fixtureApp(t)
	var logs bytes.Buffer
	application.Logger = log.New(&logs, "", 0)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	done := startApp(ctx, application)
	request := nextRequest(t, ctx, client)
	request.reply <- uploadReply{err: &leaderboard.UploadError{Message: "HTTP 408", Body: "timeout\ntry again\x1b"}}
	status := awaitStatus(t, ctx, application, "HTTP 408")
	cancel()
	<-done

	if !strings.Contains(logs.String(), `Upload error response body: "timeout\ntry again\x1b"`) {
		t.Fatalf("quoted response body missing from logs: %s", logs.String())
	}
	if strings.Contains(status.Message, "try again") {
		t.Fatal("response body should be logged, not included in tray status")
	}
}

func TestAppDryRunDoesNotUploadOrPersistState(t *testing.T) {
	t.Parallel()
	application, client := fixtureApp(t)
	application.DryRun = true
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	done := startApp(ctx, application)
	_ = awaitStatus(t, ctx, application, "Dry run:")
	cancel()
	<-done
	state, err := storage.LoadUploadState(application.Dir)

	if err != nil {
		t.Fatal(err)
	}
	if state.Fingerprint != "" || len(client.requests) != 0 {
		t.Fatal("dry run uploaded or persisted successful state")
	}
}

func TestAppCancelsInFlightUploadOnShutdown(t *testing.T) {
	t.Parallel()
	application, client := fixtureApp(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	done := startApp(ctx, application)
	_ = nextRequest(t, ctx, client)
	cancel()
	<-done
	state, err := storage.LoadUploadState(application.Dir)

	if err != nil {
		t.Fatal(err)
	}
	if state.Fingerprint != "" {
		t.Fatal("cancelled upload persisted as successful")
	}
}

func TestAppRetriesTransientFailure(t *testing.T) {
	t.Parallel()
	application, client := fixtureApp(t)
	snapshot := readSnapshot(t, application.Config.SaveFolder)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	done := startApp(ctx, application)
	first := nextRequest(t, ctx, client)
	first.reply <- uploadReply{err: &leaderboard.UploadError{Message: "temporary failure", Retry: true}}
	second := nextRequest(t, ctx, client)
	second.reply <- uploadReply{result: successfulResult()}
	_ = awaitStatus(t, ctx, application, "successfully")
	cancel()
	<-done
	state, err := storage.LoadUploadState(application.Dir)

	if err != nil {
		t.Fatal(err)
	}
	if state.Fingerprint != snapshot.Fingerprint {
		t.Fatal("retry success was not persisted")
	}
	if !reflect.DeepEqual(first.payload, second.payload) {
		t.Fatal("retry unexpectedly changed the snapshot")
	}
}

func TestAppDetectsChangesMadeDuringUpload(t *testing.T) {
	t.Parallel()
	application, client := fixtureApp(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	done := startApp(ctx, application)
	first := nextRequest(t, ctx, client)
	writeErr := os.WriteFile(filepath.Join(application.Config.SaveFolder, "HERO_01_123.pld"), []byte("new save contents"), 0600)
	first.reply <- uploadReply{result: successfulResult()}
	_ = awaitStatus(t, ctx, application, "successfully")
	second := nextRequest(t, ctx, client)
	second.reply <- uploadReply{result: successfulResult()}
	_ = awaitStatus(t, ctx, application, "successfully")
	cancel()
	<-done

	if writeErr != nil {
		t.Fatal(writeErr)
	}
	if first.payload.Files["HERO_01_123.pld"].Text != "save" || second.payload.Files["HERO_01_123.pld"].Text != "new save contents" {
		t.Fatal("changed save was not included in the next upload request")
	}
}

func TestToUploadRequestConvertsSnapshotFiles(t *testing.T) {
	snapshot := saves.Snapshot{
		Files: map[string]saves.File{
			"HERO_01_123.pld":        {Text: "hero", Mtime: 1700000000123},
			"ACCOUNT_player_123.pld": {Text: "account", Mtime: 1700000000456},
		},
		Fingerprint: "local-only",
	}
	want := leaderboard.UploadRequest{Files: map[string]leaderboard.UploadFile{
		"HERO_01_123.pld":        {Text: "hero", Mtime: 1700000000123},
		"ACCOUNT_player_123.pld": {Text: "account", Mtime: 1700000000456},
	}}

	request := toUploadRequest(snapshot)

	if !reflect.DeepEqual(request, want) {
		t.Fatalf("request = %+v; want %+v", request, want)
	}
}

type uploadReply struct {
	result leaderboard.Result
	err    error
}
type uploadRequest struct {
	payload leaderboard.UploadRequest
	reply   chan uploadReply
}
type fakeUploader struct{ requests chan uploadRequest }

func (f *fakeUploader) Upload(ctx context.Context, payload leaderboard.UploadRequest) (leaderboard.Result, error) {
	request := uploadRequest{payload: payload, reply: make(chan uploadReply, 1)}
	select {
	case f.requests <- request:
	case <-ctx.Done():
		return leaderboard.Result{}, ctx.Err()
	}
	select {
	case reply := <-request.reply:
		return reply.result, reply.err
	case <-ctx.Done():
		return leaderboard.Result{}, ctx.Err()
	}
}

func fixtureApp(t *testing.T) (*App, *fakeUploader) {
	t.Helper()
	folder := t.TempDir()
	for _, name := range []string{"HERO_01_123.pld", "ACCOUNT_player_123.pld"} {
		if err := os.WriteFile(filepath.Join(folder, name), []byte("save"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	client := &fakeUploader{requests: make(chan uploadRequest, 8)}
	return &App{Dir: t.TempDir(), Config: storage.Config{SaveFolder: folder, PollSeconds: 1, Automatic: true}, Client: client, Logger: log.New(io.Discard, "", 0), Commands: make(chan Command, 8), Updates: make(chan Status, 1)}, client
}

func readSnapshot(t *testing.T, folder string) saves.Snapshot {
	t.Helper()
	scanner := saves.Scanner{Folder: folder}
	_, _ = scanner.Scan(time.Now())
	snapshot, err := scanner.Scan(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func startApp(ctx context.Context, application *App) <-chan struct{} {
	done := make(chan struct{})
	go func() { defer close(done); application.Run(ctx) }()
	return done
}

func nextRequest(t *testing.T, ctx context.Context, client *fakeUploader) uploadRequest {
	t.Helper()
	select {
	case request := <-client.requests:
		return request
	case <-ctx.Done():
		t.Fatal("timed out waiting for upload")
		return uploadRequest{}
	}
}

func awaitStatus(t *testing.T, ctx context.Context, application *App, substring string) Status {
	t.Helper()
	for {
		select {
		case status := <-application.Updates:
			if strings.Contains(status.Message, substring) {
				return status
			}
		case <-ctx.Done():
			t.Fatalf("timed out waiting for status %q", substring)
			return Status{}
		}
	}
}

func successfulResult() leaderboard.Result {
	return leaderboard.Result{Player: "Player", Heroes: 1, UserHash: json.RawMessage(`"123"`)}
}
