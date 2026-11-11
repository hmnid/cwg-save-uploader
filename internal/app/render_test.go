package app

import (
	"errors"
	"testing"
	"time"

	"cwg2uploader/internal/saves"
)

func TestRenderStatus(t *testing.T) {
	folderError := errors.New("cannot read save folder")
	rejected := errors.New("invalid save")
	persistenceError := errors.New("disk unavailable")
	deadline := time.Date(2026, 1, 1, 12, 34, 56, 0, time.UTC)
	cases := []struct {
		name string
		view statusView
		want string
	}{
		{"starting", statusView{}, "Starting; automatic uploads send saves to the public CWG2 leaderboard"},
		{"automatic off", statusView{folder: pollEvent{kind: pollDisabled}}, "Automatic uploads off"},
		{"settling", statusView{folder: pollEvent{kind: pollSettling, err: saves.ErrSettling}}, saves.ErrSettling.Error()},
		{"stable", statusView{folder: pollEvent{kind: pollStable}}, "Up to date"},
		{"pending", statusView{upload: uploadEvent{kind: uploadQueued}}, "Pending changes"},
		{"active outranks scan error", statusView{folder: pollEvent{kind: pollFailed, err: folderError}, upload: uploadEvent{kind: uploadStarted}}, "Uploading saves"},
		{"retry continues with automatic off", statusView{folder: pollEvent{kind: pollDisabled}, upload: uploadEvent{kind: uploadRetryScheduled, retryAt: deadline}}, "Pending changes; retry at 12:34:56"},
		{"scan error survives completion", statusView{folder: pollEvent{kind: pollFailed, err: folderError}, upload: uploadEvent{kind: uploadSucceeded, result: successfulResult()}}, "cannot read save folder"},
		{"rejection remains visible with automatic off", statusView{folder: pollEvent{kind: pollDisabled}, upload: uploadEvent{kind: uploadRejected, err: rejected}}, "invalid save; -save ingame or choose Upload now"},
		{"persistence warning", statusView{upload: uploadEvent{kind: uploadSucceeded}, persistenceError: persistenceError}, "Uploaded, but could not persist successful state: disk unavailable"},
		{"successful upload", statusView{upload: uploadEvent{kind: uploadSucceeded, result: successfulResult()}}, "Uploaded 1 heroes successfully"},
		{"dry run", statusView{upload: uploadEvent{kind: uploadPreviewed, update: fixtureFolderUpdate("preview", "save")}}, "Dry run: 1 eligible files; no upload or successful state written"},
		{"command error", statusView{commandError: errors.New("cannot change folder"), upload: uploadEvent{kind: uploadStarted}}, "cannot change folder"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			message := renderStatus(tc.view)

			if message != tc.want {
				t.Fatalf("message = %q; want %q", message, tc.want)
			}
		})
	}
}
