package app

import (
	"context"
	"errors"
	"log"
	"math/rand/v2"
	"sync"
	"time"

	"cwg2uploader/internal/leaderboard"
	"cwg2uploader/internal/saves"
)

// snapshotUploader owns delivery state. It reports typed events, without
// depending on the coordinator, persistence, or presentation.
type snapshotUploader struct {
	client    Uploader
	dryRun    bool
	logger    *log.Logger
	emit      func(uploadEvent)
	results   chan uploadResult
	workers   sync.WaitGroup
	pending   *folderUpdate
	busy      bool
	rejection *rejection
	retryAt   time.Time
	attempt   int
}

type rejection struct {
	fingerprint string
	message     string
}

func (u *snapshotUploader) queue(update folderUpdate) {
	u.pending = &update
	u.rejection = nil
	u.logger.Printf("snapshot queued: %d eligible files", len(update.snapshot.Files))
	if !u.busy {
		u.emit(uploadEvent{kind: uploadQueued, update: update})
	}
}

func (u *snapshotUploader) uploadPending(ctx context.Context, now time.Time) {
	if ctx.Err() != nil || u.busy || u.pending == nil {
		return
	}
	update := *u.pending
	if now.Before(u.retryAt) {
		u.emit(uploadEvent{kind: uploadRetryScheduled, retryAt: u.retryAt})
		return
	}
	if u.dryRun {
		u.pending = nil
		u.logger.Printf("dry-run snapshot prepared: %d eligible files", len(update.snapshot.Files))
		u.emit(uploadEvent{kind: uploadPreviewed, update: update})
		return
	}
	u.pending = nil
	u.startUpload(ctx, update)
}

func (u *snapshotUploader) startUpload(ctx context.Context, update folderUpdate) {
	u.rejection = nil
	u.busy = true
	u.logger.Printf("upload started: %d eligible files", len(update.snapshot.Files))
	u.emit(uploadEvent{kind: uploadStarted, update: update})
	u.workers.Add(1)
	go func() {
		defer u.workers.Done()
		result, err := u.client.Upload(ctx, toUploadRequest(update.snapshot))
		u.results <- uploadResult{update: update, result: result, err: err}
	}()
}

func (u *snapshotUploader) handleUploadResult(response uploadResult) {
	u.busy = false
	if response.err != nil {
		u.handleUploadFailure(response)
		return
	}
	u.clearRetry()
	u.logger.Printf("upload succeeded: %d heroes", response.result.Heroes)
	u.emit(uploadEvent{kind: uploadSucceeded, update: response.update, result: response.result, at: time.Now()})
}

func (u *snapshotUploader) handleUploadFailure(response uploadResult) {
	u.logger.Printf("upload failed: %v", response.err)
	var uploadError *leaderboard.UploadError
	retryable := errors.As(response.err, &uploadError) && uploadError.Retry
	if uploadError != nil && uploadError.Body != "" {
		u.logger.Printf("Upload error response body: %q", uploadError.Body)
	}
	if retryable {
		// Prefer newer pending data, but retry even when no new event arrives.
		if u.pending == nil {
			u.queue(response.update)
		}
		delay := backoff(u.attempt)
		u.attempt++
		if uploadError.After > delay {
			delay = uploadError.After
		}
		u.retryAt = time.Now().Add(delay)
		u.logger.Printf("retry scheduled at %s", u.retryAt.Format(time.RFC3339))
		u.emit(uploadEvent{kind: uploadRetryScheduled, err: response.err, retryAt: u.retryAt})
		return
	}
	if u.pending != nil {
		// The failed snapshot has already been superseded; retain only the log.
		u.emit(uploadEvent{kind: uploadQueued, update: *u.pending})
		return
	}
	u.rejection = &rejection{fingerprint: response.update.snapshot.Fingerprint, message: response.err.Error()}
	u.emit(uploadEvent{kind: uploadRejected, update: response.update, err: response.err})
}

func (u *snapshotUploader) canReset() error {
	if u.busy {
		return errors.New("cannot reset delivery while an upload is in progress")
	}
	return nil
}

func (u *snapshotUploader) reset() error {
	if err := u.canReset(); err != nil {
		return err
	}
	u.pending = nil
	u.clearRetry()
	u.logger.Print("pending delivery state reset")
	u.emit(uploadEvent{kind: uploadIdle})
	return nil
}

func (u *snapshotUploader) clearRetry() {
	u.rejection = nil
	u.retryAt, u.attempt = time.Time{}, 0
}

func (u *snapshotUploader) wait() { u.workers.Wait() }

func newSnapshotUploader(client Uploader, dryRun bool, logger *log.Logger, emit func(uploadEvent)) *snapshotUploader {
	return &snapshotUploader{client: client, dryRun: dryRun, logger: logger, emit: emit, results: make(chan uploadResult, 1)}
}

func toUploadRequest(snapshot saves.Snapshot) leaderboard.UploadRequest {
	files := make(map[string]leaderboard.UploadFile, len(snapshot.Files))
	for name, file := range snapshot.Files {
		files[name] = leaderboard.UploadFile{Text: file.Text, Mtime: file.Mtime}
	}
	return leaderboard.UploadRequest{Files: files}
}

type uploadResult struct {
	update folderUpdate
	result leaderboard.Result
	err    error
}

func backoff(attempt int) time.Duration {
	if attempt > 6 {
		attempt = 6
	}
	delay := 5 * time.Second * time.Duration(1<<attempt)
	if delay > 5*time.Minute {
		delay = 5 * time.Minute
	}
	return delay/2 + time.Duration(rand.Int64N(int64(delay/2)+1))
}
