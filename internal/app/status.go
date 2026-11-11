package app

import (
	"time"

	"cwg2uploader/internal/storage"
)

type Status struct {
	Message    string
	LastUpload time.Time
	Config     storage.Config
}

type Command struct {
	Kind  string
	Value string
}

func (s *runState) refreshStatus() {
	s.publish(renderStatus(s.view))
}

func (s *runState) reportCommandError(err error) {
	s.logger.Printf("command failed: %v", err)
	s.view.commandError = err
	s.refreshStatus()
}

func (s *runState) publish(message string) {
	s.lastMessage = message
	status := Status{Message: message, Config: s.app.Config, LastUpload: s.app.LastUpload.UploadedAt}
	select {
	case s.app.Updates <- status:
	default:
		// Replace an unread update rather than blocking the event loop.
		select {
		case <-s.app.Updates:
		default:
		}
		select {
		case s.app.Updates <- status:
		default:
		}
	}
}
