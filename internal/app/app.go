package app

import (
	"context"
	"log"
	"strings"
	"time"

	"cwg2uploader/internal/leaderboard"
	"cwg2uploader/internal/storage"
)

type Uploader interface {
	Upload(context.Context, leaderboard.UploadRequest) (leaderboard.Result, error)
}

type App struct {
	Dir                  string
	Config               storage.Config
	LastUpload           storage.UploadState
	Client               Uploader
	Logger               *log.Logger
	DryRun               bool
	ForceUploadRequested bool
	Commands             chan Command
	Updates              chan Status
	SetStartup           func(bool) error
}

// Run coordinates folder events, upload results, and commands. Only this event
// loop mutates poller/uploader state; network workers only return results.
func (a *App) Run(ctx context.Context) {
	state := newRunState(a)
	ticker := time.NewTicker(time.Duration(a.Config.PollSeconds) * time.Second)
	defer ticker.Stop()
	defer state.uploader.workers.Wait()

	state.publish("Starting; automatic uploads send saves to the public CWG2 leaderboard")
	state.tick(ctx, time.Now())
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			state.tick(ctx, now)
		case update := <-state.poller.updates:
			state.uploader.queue(update)
			state.dispatchUploads(ctx, time.Now())
		case response := <-state.uploader.results:
			state.uploader.handleUploadResult(response)
			state.dispatchUploads(ctx, time.Now())
		case command := <-a.Commands:
			if err := state.handleCommand(ctx, command); err != nil {
				state.publish(err.Error())
			}
		}
	}
}

func newRunState(a *App) *runState {
	saveFolder, _ := storage.ExpandPath(a.Config.SaveFolder)
	state := &runState{app: a}
	state.resetPoller(saveFolder)
	state.uploader = &snapshotUploader{
		app:     a,
		publish: state.publish,
		results: make(chan uploadResult, 1),
	}
	return state
}

func (s *runState) resetPoller(saveFolder string) {
	s.app.ForceUploadRequested = false
	s.poller = newFolderPoller(saveFolder)
	lastUpload := s.app.LastUpload
	if !s.app.DryRun && lastUpload.Destination == leaderboard.BaseURL && strings.EqualFold(lastUpload.SaveFolder, saveFolder) {
		s.poller.lastEmittedFingerprint = lastUpload.Fingerprint
	}
}

// tick advances detection and retry scheduling independently. An unchanged or
// temporarily unreadable folder must not prevent an already queued retry.
func (s *runState) tick(ctx context.Context, now time.Time) {
	// Keep the poller's request latched until a stable snapshot is emitted.
	s.poller.forceNextEmission, s.app.ForceUploadRequested = s.poller.forceNextEmission || s.app.ForceUploadRequested, false
	s.pollFolder(ctx, now)
	s.dispatchUploads(ctx, now)
}

func (s *runState) pollFolder(ctx context.Context, now time.Time) {
	if ctx.Err() != nil {
		return
	}
	emitted, err := s.poller.poll(ctx, now, s.app.Config.Automatic)
	if err != nil {
		if !s.uploader.busy {
			s.publish(err.Error())
		}
		return
	}
	if !emitted && !s.uploader.busy && s.uploader.pending == nil {
		if s.app.Config.Automatic {
			s.uploader.publishIdle()
		} else {
			s.publish("Automatic uploads off")
		}
	}
}

func (s *runState) deliverReadyUpdate() {
	select {
	case update := <-s.poller.updates:
		s.uploader.queue(update)
	default:
	}
}

// Accepted work runs independently of whether automatic polling is enabled.
func (s *runState) dispatchUploads(ctx context.Context, now time.Time) {
	if ctx.Err() != nil {
		return
	}
	// Deliver an already-ready snapshot before selecting pending data to upload.
	// This is nonblocking: existing work never waits for a future emission.
	s.deliverReadyUpdate()
	if s.uploader.busy || s.uploader.pending != nil {
		s.uploader.uploadPending(ctx, now)
	}
}

type runState struct {
	app         *App
	poller      *folderPoller
	uploader    *snapshotUploader
	lastMessage string
}
