package app

import "fmt"

type statusView struct {
	folder           pollEvent
	upload           uploadEvent
	persistenceError error
	commandError     error
}

// renderStatus is the only place that turns component state into UI messages.
// Operational logs are written independently by each component.
func renderStatus(view statusView) string {
	if view.commandError != nil {
		return view.commandError.Error()
	}
	switch event := view.upload; event.kind {
	case uploadStarted:
		return "Uploading saves"
	case uploadQueued:
		return "Pending changes"
	case uploadRetryScheduled:
		return "Pending changes; retry at " + event.retryAt.Format("15:04:05")
	}
	if view.folder.kind == pollFailed {
		return view.folder.err.Error()
	}
	if view.upload.kind == uploadRejected {
		return view.upload.err.Error() + "; -save ingame or choose Upload now"
	}
	if view.persistenceError != nil {
		return "Uploaded, but could not persist successful state: " + view.persistenceError.Error()
	}
	if view.upload.kind == uploadPreviewed {
		return fmt.Sprintf("Dry run: %d eligible files; no upload or successful state written", len(view.upload.update.snapshot.Files))
	}
	if view.upload.kind == uploadSucceeded {
		return fmt.Sprintf("Uploaded %d heroes successfully", view.upload.result.Heroes)
	}
	if view.folder.kind == pollSettling {
		return view.folder.err.Error()
	}
	switch view.folder.kind {
	case pollDisabled:
		return "Automatic uploads off"
	case pollStable:
		return "Up to date"
	case pollUpdated:
		return "Pending changes"
	default:
		return "Starting; automatic uploads send saves to the public CWG2 leaderboard"
	}
}
