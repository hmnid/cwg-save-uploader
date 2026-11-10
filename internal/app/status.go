package app

import (
	"cwg2uploader/internal/storage"
	"time"
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
