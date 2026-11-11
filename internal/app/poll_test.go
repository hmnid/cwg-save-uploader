package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cwg2uploader/internal/leaderboard"
	"cwg2uploader/internal/saves"
	"cwg2uploader/internal/storage"
)

func TestFolderPollerEmitsStableSnapshotOnlyOnce(t *testing.T) {
	application, _ := fixtureApp(t)
	poller := fixtureFolderPoller(application)
	now := time.Now()

	settling := poller.poll(context.Background(), now, true)
	first := poller.poll(context.Background(), now.Add(5*time.Second), true)
	update := nextFolderUpdate(t, poller)
	repeated := poller.poll(context.Background(), now.Add(10*time.Second), true)

	if settling.kind != pollSettling || !errors.Is(settling.err, saves.ErrSettling) {
		t.Fatalf("settling event = %+v", settling)
	}
	if first.kind != pollUpdated || repeated.kind != pollStable || len(poller.updates) != 0 {
		t.Fatal("unchanged snapshot emitted more than once")
	}
	if update.snapshot.Fingerprint == "" || poller.lastEmittedFingerprint != update.snapshot.Fingerprint {
		t.Fatal("emitted fingerprint not tracked")
	}
}

func TestFolderPollerForcedEmissionReemitsUnchangedSnapshot(t *testing.T) {
	application, _ := fixtureApp(t)
	poller := fixtureFolderPoller(application)
	now := time.Now()

	poller.poll(context.Background(), now, true)
	first := poller.poll(context.Background(), now.Add(5*time.Second), true)
	firstUpdate := nextFolderUpdate(t, poller)
	poller.requestEmission()
	second := poller.poll(context.Background(), now.Add(6*time.Second), false)
	secondUpdate := nextFolderUpdate(t, poller)

	if first.kind != pollUpdated || second.kind != pollUpdated || poller.forceNextEmission {
		t.Fatal("forced emission was not consumed")
	}
	if firstUpdate.snapshot.Fingerprint != secondUpdate.snapshot.Fingerprint {
		t.Fatal("unchanged data should retain its fingerprint")
	}
}

func TestFolderPollerCoalescesUndeliveredEvents(t *testing.T) {
	application, _ := fixtureApp(t)
	poller := fixtureFolderPoller(application)
	poller.requestEmission()
	now := time.Now()
	path := filepath.Join(application.Config.SaveFolder, "HERO_01_123.pld")

	poller.poll(context.Background(), now, true)
	first := poller.poll(context.Background(), now.Add(5*time.Second), true)
	writeErr := os.WriteFile(path, []byte("newer save data"), 0600)
	poller.poll(context.Background(), now.Add(6*time.Second), true)
	second := poller.poll(context.Background(), now.Add(11*time.Second), true)
	latest := nextFolderUpdate(t, poller)

	if writeErr != nil {
		t.Fatal(writeErr)
	}
	if first.kind != pollUpdated || second.kind != pollUpdated {
		t.Fatalf("events: %+v, %+v", first, second)
	}
	if latest.snapshot.Files["HERO_01_123.pld"].Text != "newer save data" || len(poller.updates) != 0 {
		t.Fatal("latest snapshot was not retained")
	}
	if poller.forceNextEmission {
		t.Fatal("successful emission did not clear the poller's force flag")
	}
}

func TestFolderPollerAutomaticOffDoesNotEmitFurtherChanges(t *testing.T) {
	application, _ := fixtureApp(t)
	poller := fixtureFolderPoller(application)
	poller.requestEmission()
	now := time.Now()

	poller.poll(context.Background(), now, false)
	first := poller.poll(context.Background(), now.Add(5*time.Second), false)
	update := nextFolderUpdate(t, poller)
	writeErr := os.WriteFile(filepath.Join(application.Config.SaveFolder, "HERO_01_123.pld"), []byte("changed save"), 0600)
	disabled := poller.poll(context.Background(), now.Add(6*time.Second), false)

	if writeErr != nil {
		t.Fatal(writeErr)
	}
	if first.kind != pollUpdated || disabled.kind != pollDisabled {
		t.Fatalf("events: %+v, %+v", first, disabled)
	}
	if len(poller.updates) != 0 || poller.lastEmittedFingerprint != update.snapshot.Fingerprint {
		t.Fatal("manual emission enabled subsequent automatic polling")
	}
}

func TestFolderPollerCancellationDoesNotRecordEmission(t *testing.T) {
	application, _ := fixtureApp(t)
	poller := fixtureFolderPoller(application)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	event := poller.poll(ctx, time.Now(), true)

	if event.kind != pollCancelled || !errors.Is(event.err, context.Canceled) {
		t.Fatalf("cancelled poll = %+v", event)
	}
	if poller.lastEmittedFingerprint != "" || len(poller.updates) != 0 {
		t.Fatal("cancelled poll recorded an event")
	}
}

func TestCoordinatorDetectsChangesDuringAnActiveUpload(t *testing.T) {
	application, client := fixtureApp(t)
	state := newRunState(application)
	now := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer func() { cancel(); state.uploader.wait() }()

	state.tick(ctx, now)
	state.tick(ctx, now.Add(5*time.Second))
	firstFingerprint := state.poller.lastEmittedFingerprint
	_ = nextRequest(t, ctx, client)
	writeErr := os.WriteFile(filepath.Join(application.Config.SaveFolder, "HERO_01_123.pld"), []byte("changed during upload"), 0600)
	state.tick(ctx, now.Add(6*time.Second))
	state.tick(ctx, now.Add(11*time.Second))
	latest := state.uploader.pending

	if writeErr != nil {
		t.Fatal(writeErr)
	}
	if !state.uploader.busy {
		t.Fatal("test requires the original upload to remain in flight")
	}
	if latest == nil || latest.snapshot.Files["HERO_01_123.pld"].Text != "changed during upload" {
		t.Fatal("active upload prevented folder change detection")
	}
	if latest.snapshot.Fingerprint == firstFingerprint {
		t.Fatal("changed snapshot fingerprint was not emitted")
	}
}

func TestCoordinatorDeliversNewEventBeforeDispatchingOlderRetry(t *testing.T) {
	application, client := fixtureApp(t)
	state := newRunState(application)
	now := time.Now()
	state.uploader.retryAt = now.Add(10 * time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer func() { cancel(); state.uploader.wait() }()

	state.tick(ctx, now)
	state.tick(ctx, now.Add(5*time.Second))
	writeErr := os.WriteFile(filepath.Join(application.Config.SaveFolder, "HERO_01_123.pld"), []byte("latest data"), 0600)
	state.tick(ctx, now.Add(6*time.Second))
	state.tick(ctx, now.Add(11*time.Second))
	request := nextRequest(t, ctx, client)

	if writeErr != nil {
		t.Fatal(writeErr)
	}
	if request.payload.Files["HERO_01_123.pld"].Text != "latest data" {
		t.Fatal("stale pending snapshot was uploaded")
	}
}

func TestFolderPollerEmitsAgainAfterRestart(t *testing.T) {
	application, _ := fixtureApp(t)
	first := fixtureFolderPoller(application)
	restarted := fixtureFolderPoller(application)
	now := time.Now()

	first.poll(context.Background(), now, true)
	before := first.poll(context.Background(), now.Add(5*time.Second), true)
	beforeRestart := nextFolderUpdate(t, first)
	restarted.poll(context.Background(), now, true)
	after := restarted.poll(context.Background(), now.Add(5*time.Second), true)
	afterRestart := nextFolderUpdate(t, restarted)

	if before.kind != pollUpdated || after.kind != pollUpdated {
		t.Fatalf("events: %+v, %+v", before, after)
	}
	if beforeRestart.snapshot.Fingerprint != afterRestart.snapshot.Fingerprint {
		t.Fatal("restart suppressed an emitted but unacknowledged snapshot")
	}
}

func TestRunStateSeedsPollerFromMatchingSuccessfulUpload(t *testing.T) {
	cases := []struct {
		name, folderSuffix, destination string
		dryRun                          bool
		want                            string
	}{
		{"matching success", "", leaderboard.BaseURL, false, "uploaded"},
		{"different folder", "other", leaderboard.BaseURL, false, ""},
		{"different destination", "", "https://other.invalid", false, ""},
		{"dry run still emits", "", leaderboard.BaseURL, true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			application, _ := fixtureApp(t)
			application.DryRun = tc.dryRun
			application.LastUpload = storage.UploadState{SaveFolder: filepath.Join(application.Config.SaveFolder, tc.folderSuffix), Destination: tc.destination, Fingerprint: "uploaded"}

			state := newRunState(application)

			if state.poller.lastEmittedFingerprint != tc.want {
				t.Fatalf("baseline = %q; want %q", state.poller.lastEmittedFingerprint, tc.want)
			}
		})
	}
}

func TestCoordinatorAutomaticOffFinishesQueuedWorkAndRetries(t *testing.T) {
	application, client := fixtureApp(t)
	application.Config.Automatic = false
	state := newRunState(application)
	state.uploader.queue(fixtureFolderUpdate("pending", "save"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer func() { cancel(); state.uploader.wait() }()

	state.dispatchUploads(ctx, time.Now())
	first := nextRequest(t, ctx, client)
	first.reply <- uploadReply{err: &leaderboard.UploadError{Message: "temporary failure", Retry: true}}
	state.uploader.handleUploadResult(nextUploadResult(t, ctx, state.uploader))
	state.tick(ctx, state.uploader.retryAt)
	second := nextRequest(t, ctx, client)
	second.reply <- uploadReply{result: successfulResult()}
	state.uploader.handleUploadResult(nextUploadResult(t, ctx, state.uploader))

	if application.Config.Automatic {
		t.Fatal("finishing queued work enabled automatic uploads")
	}
	if application.LastUpload.Fingerprint != "pending" {
		t.Fatal("queued work did not finish with automatic uploads off")
	}
	if state.poller.lastEmittedFingerprint != "" || len(state.poller.updates) != 0 {
		t.Fatal("retry required another folder event")
	}
}

func TestTickConsumesAppForceUploadRequest(t *testing.T) {
	state := fixtureRunState(t)
	state.app.ForceUploadRequested = true

	state.tick(context.Background(), time.Now())

	if state.app.ForceUploadRequested {
		t.Fatal("tick did not consume the app request")
	}
	if !state.poller.forceNextEmission {
		t.Fatal("tick did not transfer the request to the poller")
	}
	if state.lastMessage != saves.ErrSettling.Error() {
		t.Fatal("requested snapshot bypassed settling")
	}
}

func TestTickPreservesForcedEmissionAcrossSettlingTicks(t *testing.T) {
	cases := []struct {
		name      string
		automatic bool
	}{{"automatic uploads on", true}, {"automatic uploads off", false}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := fixtureRunState(t)
			state.app.Config.Automatic = tc.automatic
			state.app.ForceUploadRequested = true
			snapshot := readSnapshot(t, state.app.Config.SaveFolder)
			state.poller.lastEmittedFingerprint = snapshot.Fingerprint
			now := time.Now()
			state.uploader.retryAt = now.Add(time.Hour)

			state.tick(context.Background(), now)
			consumed := !state.app.ForceUploadRequested
			state.tick(context.Background(), now.Add(time.Second))
			state.tick(context.Background(), now.Add(2*time.Second))
			latchedBeforeSettling := state.poller.forceNextEmission
			pendingBeforeSettling := state.uploader.pending
			state.tick(context.Background(), now.Add(5*time.Second))

			if !consumed || !latchedBeforeSettling {
				t.Fatal("later settling ticks lost the consumed force request")
			}
			if pendingBeforeSettling != nil {
				t.Fatal("requested snapshot emitted before settling")
			}
			if state.poller.forceNextEmission || state.app.ForceUploadRequested {
				t.Fatal("successful emission retained the force request")
			}
			if state.uploader.pending == nil || state.uploader.pending.snapshot.Fingerprint != snapshot.Fingerprint {
				t.Fatal("unchanged requested snapshot was not emitted after settling")
			}
			if state.app.Config.Automatic != tc.automatic {
				t.Fatal("explicit request changed the automatic upload preference")
			}
		})
	}
}

func TestPollFolderForcedEmissionWorksWithAutomaticUploadsOff(t *testing.T) {
	state := fixtureRunState(t)
	state.app.Config.Automatic = false
	state.poller.requestEmission()
	now := time.Now()

	state.pollFolder(context.Background(), now)
	state.pollFolder(context.Background(), now.Add(5*time.Second))

	if len(state.poller.updates) != 1 || state.poller.forceNextEmission {
		t.Fatal("forced snapshot was not emitted with automatic uploads off")
	}
	if state.app.Config.Automatic {
		t.Fatal("forced emission enabled automatic polling")
	}
}

func TestPollFolderAutomaticOffDoesNotReadSaveFolder(t *testing.T) {
	state := fixtureRunState(t)
	state.app.Config.Automatic = false
	state.poller.scanner.Folder = filepath.Join(t.TempDir(), "missing")

	state.pollFolder(context.Background(), time.Now())

	if state.lastMessage != "Automatic uploads off" {
		t.Fatalf("status = %q", state.lastMessage)
	}
	if len(state.poller.updates) != 0 {
		t.Fatal("disabled polling emitted an event")
	}
}

func fixtureFolderPoller(application *App) *folderPoller {
	return newFolderPoller(application.Config.SaveFolder, "", componentLogger(application.Logger, "poller"))
}

func nextFolderUpdate(t *testing.T, poller *folderPoller) folderUpdate {
	t.Helper()
	select {
	case update := <-poller.updates:
		return update
	default:
		t.Fatal("expected a folder update event")
		return folderUpdate{}
	}
}
