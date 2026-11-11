package app

import (
	"context"
	"testing"
	"time"

	"cwg2uploader/internal/leaderboard"
)

func TestDispatchUploadsRetriesWithoutWaitingForForcedEmission(t *testing.T) {
	application, client := fixtureApp(t)
	application.Config.Automatic = false
	state := newRunState(application)
	state.poller.forceNextEmission = true
	state.uploader.queue(fixtureFolderUpdate("old", "old save"))
	now := time.Now()
	state.uploader.retryAt = now
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer func() { cancel(); state.uploader.workers.Wait() }()

	state.dispatchUploads(ctx, now)
	request := nextRequest(t, ctx, client)

	if request.payload.Files["HERO_01_123.pld"].Text != "old save" {
		t.Fatal("existing retry did not run")
	}
	if !state.poller.forceNextEmission {
		t.Fatal("dispatch consumed a future emission request")
	}
}

func TestDispatchUploadsDeliversReadySnapshotBeforeRetry(t *testing.T) {
	application, client := fixtureApp(t)
	state := newRunState(application)
	state.uploader.queue(fixtureFolderUpdate("old", "old save"))
	state.poller.updates <- fixtureFolderUpdate("new", "new save")
	now := time.Now()
	state.uploader.retryAt = now
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer func() { cancel(); state.uploader.workers.Wait() }()

	state.dispatchUploads(ctx, now)
	request := nextRequest(t, ctx, client)

	if request.payload.Files["HERO_01_123.pld"].Text != "new save" {
		t.Fatal("due retry used stale data despite a ready event")
	}
}

func TestDispatchUploadsReadySnapshotStillRespectsRetryAfter(t *testing.T) {
	application, client := fixtureApp(t)
	state := newRunState(application)
	state.uploader.queue(fixtureFolderUpdate("old", "old save"))
	state.poller.updates <- fixtureFolderUpdate("new", "new save")
	now := time.Now()
	deadline := now.Add(time.Hour)
	state.uploader.retryAt = deadline
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer func() { cancel(); state.uploader.workers.Wait() }()

	state.dispatchUploads(ctx, now)

	if state.uploader.busy || len(client.requests) != 0 {
		t.Fatal("new event bypassed Retry-After")
	}
	if state.uploader.pending == nil || state.uploader.pending.snapshot.Fingerprint != "new" {
		t.Fatal("ready event was not delivered while waiting to retry")
	}
	if !state.uploader.retryAt.Equal(deadline) {
		t.Fatal("ready event changed the retry deadline")
	}
}

func TestDispatchUploadsAcceptedEventRunsWithAutomaticUploadsOff(t *testing.T) {
	application, client := fixtureApp(t)
	application.Config.Automatic = false
	state := newRunState(application)
	state.poller.forceNextEmission = true
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer func() { cancel(); state.uploader.workers.Wait() }()

	state.dispatchUploads(ctx, time.Now())
	beforeEvent := state.uploader.busy
	state.poller.updates <- fixtureFolderUpdate("requested", "save")
	state.dispatchUploads(ctx, time.Now())
	request := nextRequest(t, ctx, client)
	request.reply <- uploadReply{result: successfulResult()}
	state.uploader.handleUploadResult(nextUploadResult(t, ctx, state.uploader))
	state.dispatchUploads(ctx, time.Now())

	if beforeEvent {
		t.Fatal("dispatch started without a snapshot")
	}
	if application.Config.Automatic {
		t.Fatal("explicit upload enabled automatic uploads")
	}
	if application.LastUpload.Fingerprint != "requested" {
		t.Fatal("ready event was blocked by poller state")
	}
	if !state.poller.forceNextEmission {
		t.Fatal("dispatch changed the poller's force latch")
	}
}

func TestDispatchUploadsExistingRetryContinuesWhileRequestedSnapshotSettles(t *testing.T) {
	application, client := fixtureApp(t)
	application.Config.Automatic = false
	state := newRunState(application)
	state.uploader.queue(fixtureFolderUpdate("old", "old save"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer func() { cancel(); state.uploader.workers.Wait() }()

	commandErr := state.handleCommand(ctx, Command{Kind: "upload"})
	first := nextRequest(t, ctx, client)
	first.reply <- uploadReply{result: successfulResult()}
	state.uploader.handleUploadResult(nextUploadResult(t, ctx, state.uploader))
	state.dispatchUploads(ctx, time.Now())
	requestedBeforeFreshEvent := state.poller.forceNextEmission
	state.tick(ctx, time.Now().Add(6*time.Second))
	second := nextRequest(t, ctx, client)
	second.reply <- uploadReply{result: successfulResult()}
	state.uploader.handleUploadResult(nextUploadResult(t, ctx, state.uploader))
	state.dispatchUploads(ctx, time.Now())

	if commandErr != nil {
		t.Fatal(commandErr)
	}
	if first.payload.Files["HERO_01_123.pld"].Text != "old save" {
		t.Fatal("existing retry did not continue")
	}
	if !requestedBeforeFreshEvent {
		t.Fatal("old upload completion consumed the fresh snapshot request")
	}
	if second.payload.Files["HERO_01_123.pld"].Text != "save" || state.poller.forceNextEmission {
		t.Fatal("fresh snapshot request did not finish")
	}
}

func TestDispatchUploadsReadyEventIsNotBlockedByAutomaticPreference(t *testing.T) {
	application, client := fixtureApp(t)
	application.Config.Automatic = false
	state := newRunState(application)
	state.poller.updates <- fixtureFolderUpdate("pending", "save")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer func() { cancel(); state.uploader.workers.Wait() }()

	state.dispatchUploads(ctx, time.Now())
	request := nextRequest(t, ctx, client)
	request.reply <- uploadReply{result: successfulResult()}
	state.uploader.handleUploadResult(nextUploadResult(t, ctx, state.uploader))

	if request.payload.Files["HERO_01_123.pld"].Text != "save" || application.LastUpload.Fingerprint != "pending" {
		t.Fatal("accepted work did not finish with automatic uploads off")
	}
	if application.Config.Automatic {
		t.Fatal("finishing accepted work enabled automatic uploads")
	}
}

func TestUploadCommandOlderReadyEventDoesNotConsumeFreshRequest(t *testing.T) {
	application, client := fixtureApp(t)
	application.Config.Automatic = false
	state := newRunState(application)
	state.poller.updates <- fixtureFolderUpdate("old", "old save")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer func() { cancel(); state.uploader.workers.Wait() }()

	commandErr := state.handleCommand(ctx, Command{Kind: "upload"})
	first := nextRequest(t, ctx, client)
	first.reply <- uploadReply{result: successfulResult()}
	state.uploader.handleUploadResult(nextUploadResult(t, ctx, state.uploader))
	state.tick(ctx, time.Now().Add(6*time.Second))
	second := nextRequest(t, ctx, client)
	second.reply <- uploadReply{result: successfulResult()}
	state.uploader.handleUploadResult(nextUploadResult(t, ctx, state.uploader))

	if commandErr != nil {
		t.Fatal(commandErr)
	}
	if first.payload.Files["HERO_01_123.pld"].Text != "old save" {
		t.Fatal("older accepted work did not continue")
	}
	if second.payload.Files["HERO_01_123.pld"].Text != "save" || application.LastUpload.Fingerprint != state.poller.lastEmittedFingerprint {
		t.Fatal("older work consumed the fresh emission request")
	}
}

func TestDisablingAutomaticUploadsDoesNotCancelActiveWorkOrRetries(t *testing.T) {
	application, client := fixtureApp(t)
	state := newRunState(application)
	state.uploader.queue(fixtureFolderUpdate("accepted", "save"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer func() { cancel(); state.uploader.workers.Wait() }()

	state.dispatchUploads(ctx, time.Now())
	first := nextRequest(t, ctx, client)
	commandErr := state.handleCommand(ctx, Command{Kind: "pause"})
	first.reply <- uploadReply{err: &leaderboard.UploadError{Message: "temporary failure", Retry: true}}
	state.uploader.handleUploadResult(nextUploadResult(t, ctx, state.uploader))
	state.tick(ctx, state.uploader.retryAt)
	second := nextRequest(t, ctx, client)
	second.reply <- uploadReply{result: successfulResult()}
	state.uploader.handleUploadResult(nextUploadResult(t, ctx, state.uploader))

	if commandErr != nil {
		t.Fatal(commandErr)
	}
	if application.Config.Automatic {
		t.Fatal("automatic uploads were not turned off")
	}
	if application.LastUpload.Fingerprint != "accepted" {
		t.Fatal("turning off automatic uploads interrupted accepted work")
	}
}
