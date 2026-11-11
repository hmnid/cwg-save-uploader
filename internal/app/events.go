package app

import (
	"time"

	"cwg2uploader/internal/leaderboard"
	"cwg2uploader/internal/saves"
)

// Events describe observations and delivery transitions, never UI text.
type folderUpdate struct {
	saveFolder string
	snapshot   saves.Snapshot
}

type pollEventKind uint8

const (
	pollUnknown pollEventKind = iota
	pollDisabled
	pollSettling
	pollStable
	pollUpdated
	pollFailed
	pollCancelled
)

type pollEvent struct {
	kind pollEventKind
	err  error
}

type uploadEventKind uint8

const (
	uploadUnknown uploadEventKind = iota
	uploadIdle
	uploadQueued
	uploadStarted
	uploadRetryScheduled
	uploadRejected
	uploadSucceeded
	uploadPreviewed
)

type uploadEvent struct {
	kind    uploadEventKind
	update  folderUpdate
	result  leaderboard.Result
	err     error
	retryAt time.Time
	at      time.Time
}
