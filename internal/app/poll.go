package app

import (
	"context"
	"errors"
	"log"
	"time"

	"cwg2uploader/internal/saves"
)

// folderPoller owns detection and event deduplication, not delivery state.
type folderPoller struct {
	saveFolder             string
	scanner                *saves.Scanner
	logger                 *log.Logger
	updates                chan folderUpdate
	lastEmittedFingerprint string
	forceNextEmission      bool
	lastError              string
}

func (p *folderPoller) poll(ctx context.Context, now time.Time, automatic bool) pollEvent {
	if err := ctx.Err(); err != nil {
		return pollEvent{kind: pollCancelled, err: err}
	}
	if !automatic && !p.forceNextEmission {
		return pollEvent{kind: pollDisabled}
	}
	snapshot, err := p.scanner.Scan(now)
	if errors.Is(err, saves.ErrSettling) {
		return pollEvent{kind: pollSettling, err: err}
	}
	if err != nil {
		if err.Error() != p.lastError {
			p.logger.Printf("save scan failed: %v", err)
			p.lastError = err.Error()
		}
		return pollEvent{kind: pollFailed, err: err}
	}
	p.lastError = ""
	if snapshot.Fingerprint == p.lastEmittedFingerprint && !p.forceNextEmission {
		return pollEvent{kind: pollStable}
	}
	update := folderUpdate{saveFolder: p.saveFolder, snapshot: snapshot}
	// Keep only the latest undelivered snapshot.
	select {
	case <-p.updates:
	default:
	}
	select {
	case p.updates <- update:
		p.lastEmittedFingerprint = snapshot.Fingerprint
		p.forceNextEmission = false
		p.logger.Printf("snapshot emitted: %d eligible files", len(snapshot.Files))
		return pollEvent{kind: pollUpdated}
	case <-ctx.Done():
		return pollEvent{kind: pollCancelled, err: ctx.Err()}
	}
}

func (p *folderPoller) requestEmission() {
	p.forceNextEmission = true
}

func newFolderPoller(saveFolder, baseline string, logger *log.Logger) *folderPoller {
	return &folderPoller{
		saveFolder:             saveFolder,
		scanner:                &saves.Scanner{Folder: saveFolder, Quiet: 5 * time.Second},
		logger:                 logger,
		updates:                make(chan folderUpdate, 1),
		lastEmittedFingerprint: baseline,
	}
}
