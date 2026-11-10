package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"cwg2uploader/internal/leaderboard"
	"cwg2uploader/internal/saves"
	"cwg2uploader/internal/storage"
)

type Uploader interface {
	Upload(context.Context, saves.Snapshot) (leaderboard.Result, error)
}

type App struct {
	Dir        string
	Config     storage.Config
	State      storage.State
	Client     Uploader
	Logger     *log.Logger
	DryRun     bool
	Commands   chan Command
	Updates    chan Status
	SetStartup func(bool) error
}

// Run owns mutable application state. Upload workers only return results.
func (a *App) Run(ctx context.Context) {
	folder, _ := storage.ExpandPath(a.Config.SaveFolder)
	scanner := &saves.Scanner{Folder: folder, Quiet: 5 * time.Second}
	ticker := time.NewTicker(time.Duration(a.Config.PollSeconds) * time.Second)
	defer ticker.Stop()
	results := make(chan uploadResult, 1)
	var workers sync.WaitGroup
	defer workers.Wait()
	var busy bool
	var blocked string
	var blockedMessage string
	var retryAt time.Time
	attempt := 0
	manual := false
	lastMessage := ""
	publish := func(message string) {
		if message != lastMessage {
			a.Logger.Print(message)
			lastMessage = message
		}
		status := Status{Message: message, Config: a.Config, LastUpload: a.State.UploadedAt}
		select {
		case a.Updates <- status:
		default:
			select {
			case <-a.Updates:
			default:
			}
			select {
			case a.Updates <- status:
			default:
			}
		}
	}
	poll := func(now time.Time) {
		if ctx.Err() != nil {
			return
		}
		if busy {
			publish("Uploading saves")
			return
		}
		if !a.Config.Automatic && !manual {
			publish("Paused")
			return
		}
		snapshot, err := scanner.Scan(now)
		if err != nil {
			publish(err.Error())
			return
		}
		if a.State.Destination == leaderboard.BaseURL && strings.EqualFold(a.State.Folder, folder) && a.State.Fingerprint == snapshot.Fingerprint && !manual && !a.DryRun {
			publish("Up to date")
			return
		}
		if blocked == snapshot.Fingerprint && !manual {
			publish(blockedMessage + "; change saves or choose Upload now")
			return
		}
		if now.Before(retryAt) {
			publish("Pending changes; retry at " + retryAt.Format("15:04:05"))
			return
		}
		if a.DryRun {
			publish(fmt.Sprintf("Dry run: %d eligible files; no upload or successful state written", len(snapshot.Files)))
			manual = false
			return
		}
		busy = true
		publish("Uploading saves")
		workers.Add(1)
		uploadFolder := folder
		go func() {
			defer workers.Done()
			result, err := a.Client.Upload(ctx, snapshot)
			results <- uploadResult{snapshot: snapshot, folder: uploadFolder, result: result, err: err}
		}()
	}
	publish("Starting; automatic uploads send saves to the public CWG2 leaderboard")
	poll(time.Now())
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			poll(now)
		case response := <-results:
			busy = false
			if response.err != nil {
				var uploadError *leaderboard.UploadError
				if errors.As(response.err, &uploadError) && uploadError.Retry {
					delay := backoff(attempt)
					attempt++
					if uploadError.After > delay {
						delay = uploadError.After
					}
					retryAt = time.Now().Add(delay)
				} else {
					blocked = response.snapshot.Fingerprint
					blockedMessage = response.err.Error()
					manual = false
				}
				publish(response.err.Error())
				continue
			}
			state := storage.State{Folder: response.folder, Destination: leaderboard.BaseURL, Fingerprint: response.snapshot.Fingerprint, UploadedAt: time.Now(), Player: response.result.Player, Heroes: response.result.Heroes}
			a.State = state
			manual = false
			blocked, retryAt, attempt = "", time.Time{}, 0
			if err := storage.SaveState(a.Dir, state); err != nil {
				publish("Uploaded, but could not persist successful state: " + err.Error())
			} else {
				publish(fmt.Sprintf("Uploaded %d heroes successfully", state.Heroes))
			}
		case command := <-a.Commands:
			switch command.Kind {
			case "upload":
				manual, blocked = true, ""
				// Manual retry does not bypass Retry-After or stability checks.
			case "pause":
				config := a.Config
				config.Automatic = !config.Automatic
				if err := storage.SaveConfig(a.Dir, config); err != nil {
					publish("Save config: " + err.Error())
					continue
				}
				a.Config = config
			case "folder":
				if busy {
					publish("Wait for the current upload before changing folders")
					continue
				}
				expanded, err := storage.ExpandPath(command.Value)
				if err != nil {
					publish(err.Error())
					continue
				}
				config := a.Config
				config.SaveFolder = expanded
				if err := storage.SaveConfig(a.Dir, config); err != nil {
					publish("Save config: " + err.Error())
					continue
				}
				a.Config, folder = config, expanded
				scanner = &saves.Scanner{Folder: folder, Quiet: 5 * time.Second}
				blocked, retryAt, attempt = "", time.Time{}, 0
			case "startup":
				enabled := !a.Config.LaunchAtLogin
				if a.SetStartup == nil {
					publish("Startup integration unavailable")
					continue
				}
				if err := a.SetStartup(enabled); err != nil {
					publish("Startup registration: " + err.Error())
					continue
				}
				config := a.Config
				config.LaunchAtLogin = enabled
				if err := storage.SaveConfig(a.Dir, config); err != nil {
					_ = a.SetStartup(a.Config.LaunchAtLogin)
					publish("Save config: " + err.Error())
					continue
				}
				a.Config = config
			}
			poll(time.Now())
		}
	}
}

type uploadResult struct {
	snapshot saves.Snapshot
	folder   string
	result   leaderboard.Result
	err      error
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
