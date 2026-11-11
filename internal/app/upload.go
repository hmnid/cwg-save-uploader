package app

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	"cwg2uploader/internal/leaderboard"
	"cwg2uploader/internal/saves"
	"cwg2uploader/internal/storage"
)

// snapshotUploader owns delivery state independently of folder detection.
// Its methods run on the coordinator's event loop; only Upload runs in a worker.
type snapshotUploader struct {
	app       *App
	publish   func(string)
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
}

func (u *snapshotUploader) uploadPending(ctx context.Context, now time.Time) {
	if ctx.Err() != nil {
		return
	}
	if u.busy {
		u.publish("Uploading saves")
		return
	}
	if u.pending == nil {
		if u.rejection != nil {
			u.publishIdle()
		}
		return
	}
	update := *u.pending
	if now.Before(u.retryAt) {
		u.publish("Pending changes; retry at " + u.retryAt.Format("15:04:05"))
		return
	}
	if u.app.DryRun {
		u.pending = nil
		u.publish(fmt.Sprintf("Dry run: %d eligible files; no upload or successful state written", len(update.snapshot.Files)))
		return
	}
	u.pending = nil
	u.startUpload(ctx, update)
}

func (u *snapshotUploader) publishIdle() {
	if u.rejection != nil {
		u.publish(u.rejection.message + "; -save ingame or choose Upload now")
		return
	}
	u.publish("Up to date")
}

func (u *snapshotUploader) startUpload(ctx context.Context, update folderUpdate) {
	u.rejection = nil
	u.busy = true
	u.publish("Uploading saves")
	u.workers.Add(1)
	go func() {
		defer u.workers.Done()
		result, err := u.app.Client.Upload(ctx, toUploadRequest(update.snapshot))
		u.results <- uploadResult{update: update, result: result, err: err}
	}()
}

func (u *snapshotUploader) handleUploadResult(response uploadResult) {
	u.busy = false
	if response.err != nil {
		u.handleUploadFailure(response)
		return
	}
	u.recordUpload(response)
}

func (u *snapshotUploader) handleUploadFailure(response uploadResult) {
	var uploadError *leaderboard.UploadError
	if errors.As(response.err, &uploadError) && uploadError.Retry {
		// Retry independently of new poller events, preferring any newer pending data.
		if u.pending == nil {
			u.queue(response.update)
		}
		delay := backoff(u.attempt)
		u.attempt++
		if uploadError.After > delay {
			delay = uploadError.After
		}
		u.retryAt = time.Now().Add(delay)
	} else if u.pending == nil {
		// Retain rejection status only while this failed snapshot is current.
		// An event queued during the request already supersedes its result.
		u.rejection = &rejection{
			fingerprint: response.update.snapshot.Fingerprint,
			message:     response.err.Error(),
		}
	}
	if uploadError != nil && uploadError.Body != "" {
		u.app.Logger.Printf("Upload error response body: %q", uploadError.Body)
	}
	u.publish(response.err.Error())
}

func (u *snapshotUploader) recordUpload(response uploadResult) {
	lastUpload := storage.UploadState{
		SaveFolder:  response.update.saveFolder,
		Destination: leaderboard.BaseURL,
		Fingerprint: response.update.snapshot.Fingerprint,
		UploadedAt:  time.Now(),
		Player:      response.result.Player,
		Heroes:      response.result.Heroes,
	}
	u.app.LastUpload = lastUpload
	u.clearRetry()
	if err := storage.SaveUploadState(u.app.Dir, lastUpload); err != nil {
		u.publish("Uploaded, but could not persist successful state: " + err.Error())
	} else {
		u.publish(fmt.Sprintf("Uploaded %d heroes successfully", lastUpload.Heroes))
	}
}

func (u *snapshotUploader) clearRetry() {
	u.rejection = nil
	u.retryAt, u.attempt = time.Time{}, 0
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
