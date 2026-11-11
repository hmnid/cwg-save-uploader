package windows

import (
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"path/filepath"
	"strings"
	"unicode"

	"cwg2uploader/internal/app"
	"cwg2uploader/internal/leaderboard"
	"cwg2uploader/internal/storage"
	"github.com/getlantern/systray"
)

func RunTray(ctx context.Context, cancel context.CancelFunc, commands chan<- app.Command, updates <-chan app.Status, config storage.Config, dir string) {
	systray.Run(func() {
		systray.SetIcon(trayIcon())
		systray.SetTooltip("CWG2 Uploader")
		statusItem := systray.AddMenuItem("Starting...", "Show full status")
		lastItem := systray.AddMenuItem("Last upload: never", "")
		lastItem.Disable()
		systray.AddSeparator()
		automaticUploads := systray.AddMenuItemCheckbox("Automatic uploads", "Send new saves automatically; queued uploads and retries continue when off", config.Automatic)
		upload := systray.AddMenuItem("Upload now", "Upload current saves even with Automatic uploads off")
		folder := systray.AddMenuItem("Choose save folder...", "")
		startup := systray.AddMenuItemCheckbox("Launch at Windows sign-in", "", config.LaunchAtLogin)
		board := systray.AddMenuItem("Open leaderboard", "")
		logs := systray.AddMenuItem("Open logs", "")
		settings := systray.AddMenuItem("Open config.json (restart to reload)", "")
		systray.AddSeparator()
		quit := systray.AddMenuItem("Quit", "")
		go func() {
			latest := app.Status{Message: "Starting", Config: config}
			picked := make(chan folderResult, 1)
			picking := false
			send := func(command app.Command) {
				select {
				case commands <- command:
				case <-ctx.Done():
				}
			}
			showError := func(err error) {
				if err != nil {
					go Message("CWG2 Uploader", err.Error())
				}
			}
			for {
				select {
				case <-ctx.Done():
					systray.Quit()
					return
				case <-quit.ClickedCh:
					cancel()
					systray.Quit()
					return
				case update := <-updates:
					latest = update
					statusItem.SetTitle(display(update.Message, 90))
					systray.SetTooltip(display("CWG2: "+update.Message, 120))
					if !update.LastUpload.IsZero() {
						lastItem.SetTitle("Last upload: " + update.LastUpload.Format("2006-01-02 15:04:05"))
					}
					if update.Config.Automatic {
						automaticUploads.Check()
					} else {
						automaticUploads.Uncheck()
					}
					if update.Config.LaunchAtLogin {
						startup.Check()
					} else {
						startup.Uncheck()
					}
				case <-statusItem.ClickedCh:
					text := latest.Message + "\n\nSave folder: " + latest.Config.SaveFolder
					go Message("CWG2 Uploader status", text)
				case <-automaticUploads.ClickedCh:
					send(app.Command{Kind: "pause"})
				case <-upload.ClickedCh:
					send(app.Command{Kind: "upload"})
				case <-startup.ClickedCh:
					send(app.Command{Kind: "startup"})
				case <-folder.ClickedCh:
					if !picking {
						picking = true
						folder.Disable()
						go func() { path, err := PickFolder(); picked <- folderResult{path, err} }()
					}
				case result := <-picked:
					picking = false
					folder.Enable()
					showError(result.err)
					if result.path != "" {
						send(app.Command{Kind: "folder", Value: result.path})
					}
				case <-board.ClickedCh:
					showError(Open(leaderboard.BaseURL + "/#leaderboard"))
				case <-logs.ClickedCh:
					showError(Open(dir))
				case <-settings.ClickedCh:
					showError(Open(filepath.Join(dir, "config.json")))
				}
			}
		}()
	}, cancel)
}

type folderResult struct {
	path string
	err  error
}

func display(text string, limit int) string {
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, text)
	runes := []rune(text)
	if len(runes) > limit {
		return string(runes[:limit-1]) + "…"
	}
	return text
}

// ICO containing a generated PNG keeps distribution to one executable.
func trayIcon() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			c := color.RGBA{35, 53, 74, 255}
			if (x >= 13 && x <= 18 && y >= 6 && y <= 23) || (y >= 7 && y <= 12 && x >= 8 && x <= 23 && x+y >= 21 && y-x >= -10) || (y >= 25 && y <= 27 && x >= 7 && x <= 24) {
				c = color.RGBA{110, 230, 175, 255}
			}
			img.SetRGBA(x, y, c)
		}
	}
	var pngData bytes.Buffer
	_ = png.Encode(&pngData, img)
	header := make([]byte, 22)
	binary.LittleEndian.PutUint16(header[2:], 1)
	binary.LittleEndian.PutUint16(header[4:], 1)
	header[6], header[7] = 32, 32
	binary.LittleEndian.PutUint16(header[10:], 1)
	binary.LittleEndian.PutUint16(header[12:], 32)
	binary.LittleEndian.PutUint32(header[14:], uint32(pngData.Len()))
	binary.LittleEndian.PutUint32(header[18:], 22)
	return append(header, pngData.Bytes()...)
}
