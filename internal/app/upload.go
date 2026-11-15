package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"cwg2uploader/internal/leaderboard"
	"cwg2uploader/internal/saves"
)

var errUploaderStopped = errors.New("uploader stopped")

// snapshotUploader shares only its latest pending snapshot under mu. One worker
// owns HTTP calls, retry timing, and lifecycle events; none hold mu while waiting.
type snapshotUploader struct {
	mu       sync.Mutex
	pending  *folderUpdate
	wake     chan struct{}
	events   <-chan uploadEvent
	done     chan struct{}
	stopping <-chan struct{}
	cancel   context.CancelFunc
}

func (u *snapshotUploader) run(ctx context.Context, output chan<- uploadEvent, client Uploader, dryRun bool, logger *log.Logger) {
	defer close(u.done)
	defer close(output)
	var failed *folderUpdate
	attempt := 0
	for {
		update := u.nextSnapshot(ctx, failed)
		if update == nil {
			return
		}
		failed = nil
		workLogger := prefixedLogger(logger, fmt.Sprintf("work_id=%d ", update.id))
		if dryRun {
			workLogger.Printf("dry-run snapshot prepared: %d eligible files", len(update.snapshot.Files))
			output <- uploadPreviewed{id: update.id, saveFolder: update.saveFolder, fileCount: len(update.snapshot.Files)}
			continue
		}
		output <- uploadStarted{id: update.id, saveFolder: update.saveFolder}
		if ctx.Err() != nil {
			return
		}
		workLogger.Printf("upload started: %d eligible files", len(update.snapshot.Files))
		result, err := client.Upload(ctx, toUploadRequest(update.snapshot))
		if err == nil {
			attempt = 0
			workLogger.Printf("upload succeeded: %d heroes", result.Heroes)
			// Even during shutdown, report actual success before closing events.
			output <- uploadSucceeded{id: update.id, saveFolder: update.saveFolder, fingerprint: update.snapshot.Fingerprint, result: result, at: time.Now()}
			continue
		}
		if ctx.Err() != nil {
			workLogger.Printf("upload stopped: %v", err)
			return
		}
		logUploadFailure(workLogger, err)
		delay, retryable := retryDelay(err, attempt)
		if !retryable {
			if !u.hasPending() {
				output <- uploadRejected{id: update.id, saveFolder: update.saveFolder, err: err}
			}
			continue
		}
		failed = update
		attempt++
		retry := uploadRetryScheduled{id: update.id, saveFolder: update.saveFolder}
		if !waitForRetry(ctx, output, workLogger, retry, delay) {
			return
		}
	}
}

// queue replaces pending work without waiting for HTTP or event delivery.
// Snapshots must remain immutable; completion is reported through events.
func (u *snapshotUploader) queue(ctx context.Context, update folderUpdate) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-u.stopping:
		return errUploaderStopped
	default:
	}
	u.pending = &update
	select {
	case u.wake <- struct{}{}:
	default:
	}
	return nil
}

func (u *snapshotUploader) stop() { u.cancel() }

// Drain events through channel closure before waiting.
func (u *snapshotUploader) wait() { <-u.done }

func newSnapshotUploader(parent context.Context, client Uploader, dryRun bool, logger *log.Logger) *snapshotUploader {
	ctx, cancel := context.WithCancel(parent)
	events := make(chan uploadEvent)
	uploader := &snapshotUploader{wake: make(chan struct{}, 1), events: events, done: make(chan struct{}), stopping: ctx.Done(), cancel: cancel}
	go uploader.run(ctx, events, client, dryRun, logger)
	return uploader
}

// Prefer the latest queued snapshot over failed data after backoff expires.
func (u *snapshotUploader) nextSnapshot(ctx context.Context, failed *folderUpdate) *folderUpdate {
	for ctx.Err() == nil {
		if update := u.takePending(); update != nil {
			return update
		}
		if failed != nil {
			return failed
		}
		select {
		case <-ctx.Done():
		case <-u.wake:
		}
	}
	return nil
}

func (u *snapshotUploader) takePending() *folderUpdate {
	u.mu.Lock()
	defer u.mu.Unlock()
	update := u.pending
	u.pending = nil
	return update
}

func (u *snapshotUploader) hasPending() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.pending != nil
}

func waitForRetry(ctx context.Context, output chan<- uploadEvent, logger *log.Logger, retry uploadRetryScheduled, delay time.Duration) bool {
	retry.retryAt = time.Now().Add(delay)
	timer := time.NewTimer(delay)
	defer timer.Stop()
	logger.Printf("retry scheduled at %s", retry.retryAt.Format(time.RFC3339))
	output <- retry
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func logUploadFailure(logger *log.Logger, err error) {
	logger.Printf("upload failed: %v", err)
	var uploadError *leaderboard.UploadError
	if errors.As(err, &uploadError) && uploadError.Body != "" {
		logger.Printf("Upload error response body: %q", uploadError.Body)
	}
}

func retryDelay(err error, attempt int) (time.Duration, bool) {
	var uploadError *leaderboard.UploadError
	if !errors.As(err, &uploadError) || !uploadError.Retry {
		return 0, false
	}
	return max(backoff(attempt), uploadError.After), true
}

func toUploadRequest(snapshot saves.Snapshot) leaderboard.UploadRequest {
	files := make(map[string]leaderboard.UploadFile, len(snapshot.Files))
	for name, file := range snapshot.Files {
		// Match the website uploader's text decoding at the protocol boundary.
		text := strings.TrimPrefix(file.Text, "\xef\xbb\xbf")
		files[name] = leaderboard.UploadFile{Text: text, Mtime: file.Mtime}
	}
	return leaderboard.UploadRequest{Files: files}
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
