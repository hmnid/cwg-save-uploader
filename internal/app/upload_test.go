package app

import (
	"bytes"
	"context"
	"log"
	"reflect"
	"strings"
	"testing"
	"time"

	"cwg2uploader/internal/leaderboard"
	"cwg2uploader/internal/saves"
	"cwg2uploader/internal/storage"
)

func TestUploaderRetriesWithoutAnotherFolderEvent(t *testing.T) {
	application, client := fixtureApp(t)
	uploader := newRunState(application).uploader
	update := fixtureFolderUpdate("initial", "save")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer func() { cancel(); uploader.workers.Wait() }()

	uploader.queue(update)
	uploader.uploadPending(ctx, time.Now())
	first := nextRequest(t, ctx, client)
	first.reply <- uploadReply{err: &leaderboard.UploadError{Message: "retry later", Retry: true, After: time.Hour}}
	uploader.handleUploadResult(nextUploadResult(t, ctx, uploader))
	deadline := uploader.retryAt
	uploader.uploadPending(ctx, deadline.Add(-time.Nanosecond))
	beforeDeadline := len(client.requests)
	uploader.uploadPending(ctx, deadline)
	second := nextRequest(t, ctx, client)
	second.reply <- uploadReply{result: successfulResult()}
	uploader.handleUploadResult(nextUploadResult(t, ctx, uploader))

	if beforeDeadline != 0 {
		t.Fatal("retry started before Retry-After elapsed")
	}
	if !reflect.DeepEqual(first.payload, second.payload) {
		t.Fatal("retry changed the submitted data")
	}
	if application.LastUpload.Fingerprint != "initial" {
		t.Fatal("retry success not recorded")
	}
	if uploader.pending != nil {
		t.Fatal("successful retry retained pending work")
	}
}

func TestUploaderKeepsLatestEventWhileUploadIsActive(t *testing.T) {
	application, client := fixtureApp(t)
	uploader := newRunState(application).uploader
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer func() { cancel(); uploader.workers.Wait() }()

	uploader.queue(fixtureFolderUpdate("initial", "first"))
	uploader.uploadPending(ctx, time.Now())
	first := nextRequest(t, ctx, client)
	uploader.queue(fixtureFolderUpdate("middle", "second"))
	uploader.queue(fixtureFolderUpdate("latest", "third"))
	uploader.uploadPending(ctx, time.Now())
	whileBusy := len(client.requests)
	first.reply <- uploadReply{result: successfulResult()}
	uploader.handleUploadResult(nextUploadResult(t, ctx, uploader))
	afterFirst := application.LastUpload.Fingerprint
	uploader.uploadPending(ctx, time.Now())
	second := nextRequest(t, ctx, client)
	second.reply <- uploadReply{result: successfulResult()}
	uploader.handleUploadResult(nextUploadResult(t, ctx, uploader))

	if whileBusy != 0 {
		t.Fatal("more than one upload ran at a time")
	}
	if afterFirst != "initial" {
		t.Fatal("old result marked newer data as uploaded")
	}
	if second.payload.Files["HERO_01_123.pld"].Text != "third" {
		t.Fatal("pending events were not coalesced to the latest snapshot")
	}
	if application.LastUpload.Fingerprint != "latest" {
		t.Fatal("latest result not persisted")
	}
}

func TestUploaderRetriesLatestPendingEventAfterFailure(t *testing.T) {
	application, client := fixtureApp(t)
	uploader := newRunState(application).uploader
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer func() { cancel(); uploader.workers.Wait() }()

	uploader.queue(fixtureFolderUpdate("initial", "first"))
	uploader.uploadPending(ctx, time.Now())
	first := nextRequest(t, ctx, client)
	uploader.queue(fixtureFolderUpdate("latest", "new save"))
	first.reply <- uploadReply{err: &leaderboard.UploadError{Message: "temporary failure", Retry: true}}
	uploader.handleUploadResult(nextUploadResult(t, ctx, uploader))
	uploader.uploadPending(ctx, uploader.retryAt)
	second := nextRequest(t, ctx, client)
	second.reply <- uploadReply{result: successfulResult()}
	uploader.handleUploadResult(nextUploadResult(t, ctx, uploader))

	if second.payload.Files["HERO_01_123.pld"].Text != "new save" {
		t.Fatal("retry replaced the latest event with stale failed data")
	}
	if application.LastUpload.Fingerprint != "latest" {
		t.Fatal("latest snapshot was not recorded")
	}
}

func TestUploaderRetriesAcceptedEventIndependentlyOfAutomaticMode(t *testing.T) {
	application, client := fixtureApp(t)
	application.Config.Automatic = false
	uploader := newRunState(application).uploader
	update := fixtureFolderUpdate("requested", "save")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer func() { cancel(); uploader.workers.Wait() }()

	uploader.queue(update)
	uploader.uploadPending(ctx, time.Now())
	first := nextRequest(t, ctx, client)
	first.reply <- uploadReply{err: &leaderboard.UploadError{Message: "temporary failure", Retry: true}}
	uploader.handleUploadResult(nextUploadResult(t, ctx, uploader))
	uploader.uploadPending(ctx, uploader.retryAt)
	second := nextRequest(t, ctx, client)
	second.reply <- uploadReply{result: successfulResult()}
	uploader.handleUploadResult(nextUploadResult(t, ctx, uploader))

	if application.Config.Automatic {
		t.Fatal("uploader changed the automatic-upload preference")
	}
	if application.LastUpload.Fingerprint != "requested" {
		t.Fatal("accepted event was lost between retries")
	}
}

func TestUploaderTrustsNewEventAfterRejection(t *testing.T) {
	application, client := fixtureApp(t)
	uploader := newRunState(application).uploader
	update := fixtureFolderUpdate("rejected", "save")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer func() { cancel(); uploader.workers.Wait() }()

	uploader.queue(update)
	uploader.uploadPending(ctx, time.Now())
	first := nextRequest(t, ctx, client)
	first.reply <- uploadReply{err: &leaderboard.UploadError{Message: "invalid save"}}
	uploader.handleUploadResult(nextUploadResult(t, ctx, uploader))
	uploader.queue(update)
	uploader.uploadPending(ctx, time.Now())
	second := nextRequest(t, ctx, client)
	second.reply <- uploadReply{result: successfulResult()}
	uploader.handleUploadResult(nextUploadResult(t, ctx, uploader))

	if application.LastUpload.Fingerprint != "rejected" {
		t.Fatal("uploader ignored a newly emitted event for a previously rejected snapshot")
	}
}

func TestUploaderUploadsPreviouslySuccessfulEvent(t *testing.T) {
	application, client := fixtureApp(t)
	update := fixtureFolderUpdate("uploaded", "save")
	application.LastUpload = storage.UploadState{SaveFolder: update.saveFolder, Destination: leaderboard.BaseURL, Fingerprint: update.snapshot.Fingerprint}
	uploader := newRunState(application).uploader
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer func() { cancel(); uploader.workers.Wait() }()

	uploader.queue(update)
	uploader.uploadPending(ctx, time.Now())
	request := nextRequest(t, ctx, client)
	request.reply <- uploadReply{result: successfulResult()}
	uploader.handleUploadResult(nextUploadResult(t, ctx, uploader))

	if request.payload.Files["HERO_01_123.pld"].Text != "save" {
		t.Fatal("uploader deduplicated an event instead of trusting the poller")
	}
}

func TestUploaderNewEventClearsPreviousRejection(t *testing.T) {
	application, client := fixtureApp(t)
	update := fixtureFolderUpdate("uploaded", "save")
	application.LastUpload = storage.UploadState{SaveFolder: update.saveFolder, Destination: leaderboard.BaseURL, Fingerprint: update.snapshot.Fingerprint}
	state := newRunState(application)
	state.uploader.rejection = &rejection{fingerprint: "different rejected snapshot", message: "invalid save"}

	state.uploader.queue(update)

	if state.uploader.rejection != nil {
		t.Fatal("new event retained an obsolete rejection")
	}
	if state.uploader.pending == nil || state.uploader.pending.snapshot.Fingerprint != "uploaded" {
		t.Fatal("new event was not retained for dispatch")
	}
	if len(client.requests) != 0 {
		t.Fatal("queueing must not dispatch a network request")
	}
}

func TestUploaderPreviouslyUploadedEventStillHonorsRetryDeadline(t *testing.T) {
	application, client := fixtureApp(t)
	update := fixtureFolderUpdate("uploaded", "save")
	application.LastUpload = storage.UploadState{SaveFolder: update.saveFolder, Destination: leaderboard.BaseURL, Fingerprint: update.snapshot.Fingerprint}
	uploader := newRunState(application).uploader
	now := time.Now()
	deadline := now.Add(time.Hour)
	uploader.retryAt = deadline
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer func() { cancel(); uploader.workers.Wait() }()

	uploader.queue(update)
	uploader.uploadPending(ctx, now)
	uploader.queue(fixtureFolderUpdate("new", "changed save"))
	uploader.uploadPending(ctx, now)

	if !uploader.retryAt.Equal(deadline) || len(client.requests) != 0 {
		t.Fatal("previous upload history bypassed Retry-After for newer data")
	}
	if uploader.pending == nil || uploader.pending.snapshot.Fingerprint != "new" {
		t.Fatal("new data was not retained for a later retry")
	}
}

func TestUploaderQueuedSnapshotDoesNotInheritOlderRejection(t *testing.T) {
	application, client := fixtureApp(t)
	var logs bytes.Buffer
	application.Logger = log.New(&logs, "", 0)
	uploader := newRunState(application).uploader
	deadline := time.Now().Add(time.Hour)
	uploader.retryAt, uploader.attempt = deadline, 3
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer func() { cancel(); uploader.workers.Wait() }()

	uploader.queue(fixtureFolderUpdate("old", "old save"))
	uploader.uploadPending(ctx, deadline)
	first := nextRequest(t, ctx, client)
	uploader.queue(fixtureFolderUpdate("new", "new save"))
	first.reply <- uploadReply{err: &leaderboard.UploadError{Message: "invalid old snapshot"}}
	uploader.handleUploadResult(nextUploadResult(t, ctx, uploader))
	afterOldFailure := uploader.rejection
	uploader.uploadPending(ctx, deadline)
	second := nextRequest(t, ctx, client)
	duringNewUpload := uploader.rejection
	attempt, retryAt := uploader.attempt, uploader.retryAt
	second.reply <- uploadReply{result: successfulResult()}
	uploader.handleUploadResult(nextUploadResult(t, ctx, uploader))

	if afterOldFailure != nil || duringNewUpload != nil {
		t.Fatal("old failure became rejection state for the newer snapshot")
	}
	if attempt != 3 || !retryAt.Equal(deadline) {
		t.Fatal("discarding stale rejection changed delivery backoff")
	}
	if second.payload.Files["HERO_01_123.pld"].Text != "new save" || application.LastUpload.Fingerprint != "new" {
		t.Fatal("new snapshot was not uploaded successfully")
	}
	if !strings.Contains(logs.String(), "invalid old snapshot") {
		t.Fatal("superseded upload failure was not logged")
	}
}

func TestUploaderNewEventAfterFailureClearsRejectionWithoutResettingBackoff(t *testing.T) {
	application, client := fixtureApp(t)
	uploader := newRunState(application).uploader
	deadline := time.Now().Add(time.Hour)
	uploader.retryAt, uploader.attempt = deadline, 2
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer func() { cancel(); uploader.workers.Wait() }()

	uploader.queue(fixtureFolderUpdate("old", "old save"))
	uploader.uploadPending(ctx, deadline)
	first := nextRequest(t, ctx, client)
	first.reply <- uploadReply{err: &leaderboard.UploadError{Message: "invalid old snapshot"}}
	uploader.handleUploadResult(nextUploadResult(t, ctx, uploader))
	oldRejection := uploader.rejection
	uploader.queue(fixtureFolderUpdate("new", "new save"))
	afterQueue := uploader.rejection
	uploader.uploadPending(ctx, deadline)
	second := nextRequest(t, ctx, client)
	duringNewUpload := uploader.rejection
	attempt, retryAt := uploader.attempt, uploader.retryAt
	second.reply <- uploadReply{result: successfulResult()}
	uploader.handleUploadResult(nextUploadResult(t, ctx, uploader))

	if oldRejection == nil || oldRejection.fingerprint != "old" || oldRejection.message != "invalid old snapshot" {
		t.Fatal("idle rejection did not identify its failed snapshot")
	}
	if afterQueue != nil || duringNewUpload != nil {
		t.Fatal("new event retained the old snapshot's rejection")
	}
	if attempt != 2 || !retryAt.Equal(deadline) {
		t.Fatal("clearing rejection changed delivery backoff")
	}
	if application.LastUpload.Fingerprint != "new" {
		t.Fatal("new snapshot did not complete")
	}
}

func TestUploaderStartingNewUploadClearsRejectionWithoutResettingBackoff(t *testing.T) {
	application, client := fixtureApp(t)
	uploader := newRunState(application).uploader
	deadline := time.Now().Add(time.Hour)
	uploader.queue(fixtureFolderUpdate("new", "new save"))
	uploader.rejection = &rejection{fingerprint: "old", message: "invalid old snapshot"}
	uploader.retryAt, uploader.attempt = deadline, 4
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer func() { cancel(); uploader.workers.Wait() }()

	uploader.uploadPending(ctx, deadline)
	request := nextRequest(t, ctx, client)

	if uploader.rejection != nil {
		t.Fatal("starting a new upload retained stale rejection state")
	}
	if uploader.attempt != 4 || !uploader.retryAt.Equal(deadline) {
		t.Fatal("starting a new upload reset delivery backoff")
	}
	if request.payload.Files["HERO_01_123.pld"].Text != "new save" {
		t.Fatal("wrong snapshot was uploaded")
	}
}

func fixtureFolderUpdate(fingerprint, text string) folderUpdate {
	return folderUpdate{
		saveFolder: "saves",
		snapshot:   saves.Snapshot{Files: map[string]saves.File{"HERO_01_123.pld": {Text: text, Mtime: 1700000000123}}, Fingerprint: fingerprint},
	}
}

func nextUploadResult(t *testing.T, ctx context.Context, uploader *snapshotUploader) uploadResult {
	t.Helper()
	select {
	case result := <-uploader.results:
		return result
	case <-ctx.Done():
		t.Fatal("timed out waiting for upload result")
		return uploadResult{}
	}
}
