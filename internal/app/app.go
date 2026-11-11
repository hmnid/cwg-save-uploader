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

// Run owns coordination and presentation. Component events are handled
// synchronously on this loop; network workers only send results through a channel.
func (a *App) Run(ctx context.Context) {
	state := newRunState(a)
	ticker := time.NewTicker(time.Duration(a.Config.PollSeconds) * time.Second)
	defer ticker.Stop()
	defer state.uploader.wait()

	state.logger.Print("started")
	state.refreshStatus()
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
				state.reportCommandError(err)
			}
		}
	}
}

func newRunState(a *App) *runState {
	state := &runState{app: a, logger: componentLogger(a.Logger, "app")}
	saveFolder, _ := storage.ExpandPath(a.Config.SaveFolder)
	state.resetPoller(saveFolder)
	state.uploader = newSnapshotUploader(a.Client, a.DryRun, componentLogger(a.Logger, "uploader"), state.handleUploadEvent)
	return state
}

func (s *runState) resetPoller(saveFolder string) {
	s.app.ForceUploadRequested = false
	baseline := ""
	lastUpload := s.app.LastUpload
	if !s.app.DryRun && lastUpload.Destination == leaderboard.BaseURL && strings.EqualFold(lastUpload.SaveFolder, saveFolder) {
		baseline = lastUpload.Fingerprint
	}
	s.poller = newFolderPoller(saveFolder, baseline, componentLogger(s.app.Logger, "poller"))
	s.view.folder = pollEvent{}
}

func (s *runState) tick(ctx context.Context, now time.Time) {
	s.pollFolder(ctx, now)
	s.dispatchUploads(ctx, now)
}

func (s *runState) pollFolder(ctx context.Context, now time.Time) {
	if ctx.Err() != nil {
		return
	}
	if s.app.ForceUploadRequested {
		s.poller.requestEmission()
		s.app.ForceUploadRequested = false
	}
	observation := s.poller.poll(ctx, now, s.app.Config.Automatic)
	if observation.kind == pollCancelled {
		return
	}
	s.view.folder = observation
	s.view.commandError = nil
	s.refreshStatus()
}

func (s *runState) deliverReadyUpdate() {
	select {
	case update := <-s.poller.updates:
		s.uploader.queue(update)
	default:
	}
}

// Only ready messages affect dispatch; delivery is independent of polling state.
func (s *runState) dispatchUploads(ctx context.Context, now time.Time) {
	if ctx.Err() != nil {
		return
	}
	s.deliverReadyUpdate()
	s.uploader.uploadPending(ctx, now)
}

func (s *runState) handleUploadEvent(event uploadEvent) {
	s.view.upload = event
	s.view.commandError = nil
	if event.kind == uploadIdle {
		s.view.persistenceError = nil
	}
	if event.kind == uploadSucceeded {
		s.recordUpload(event)
	}
	s.refreshStatus()
}

func (s *runState) recordUpload(event uploadEvent) {
	lastUpload := storage.UploadState{
		SaveFolder:  event.update.saveFolder,
		Destination: leaderboard.BaseURL,
		Fingerprint: event.update.snapshot.Fingerprint,
		UploadedAt:  event.at,
		Player:      event.result.Player,
		Heroes:      event.result.Heroes,
	}
	s.app.LastUpload = lastUpload
	s.view.persistenceError = storage.SaveUploadState(s.app.Dir, lastUpload)
	if s.view.persistenceError != nil {
		s.logger.Printf("save upload state failed: %v", s.view.persistenceError)
	}
}

type runState struct {
	app         *App
	logger      *log.Logger
	poller      *folderPoller
	uploader    *snapshotUploader
	view        statusView
	lastMessage string
}
