package app

import (
	"context"
	"time"

	"cwg2uploader/internal/saves"
)

type folderUpdate struct {
	saveFolder string
	snapshot   saves.Snapshot
}

// folderPoller knows only about save files and the last event it emitted.
// The coordinator may seed its initial baseline from a successful upload;
// rejection and retry state never influence event emission.
type folderPoller struct {
	saveFolder             string
	scanner                *saves.Scanner
	updates                chan folderUpdate
	lastEmittedFingerprint string
	forceNextEmission      bool
}

// When automatic polling is off, a forced emission still waits for stable data.
func (p *folderPoller) poll(ctx context.Context, now time.Time, automatic bool) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if !automatic && !p.forceNextEmission {
		return false, nil
	}
	snapshot, err := p.scanner.Scan(now)
	if err != nil {
		return false, err
	}
	if snapshot.Fingerprint == p.lastEmittedFingerprint && !p.forceNextEmission {
		return false, nil
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
		return true, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

func newFolderPoller(saveFolder string) *folderPoller {
	return &folderPoller{
		saveFolder: saveFolder,
		scanner:    &saves.Scanner{Folder: saveFolder, Quiet: 5 * time.Second},
		updates:    make(chan folderUpdate, 1),
	}
}
