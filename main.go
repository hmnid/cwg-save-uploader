//go:build windows

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"

	"cwg2uploader/internal/app"
	"cwg2uploader/internal/leaderboard"
	"cwg2uploader/internal/storage"
	ui "cwg2uploader/internal/windows"
)

func main() {
	console := flag.Bool("console", false, "run without the tray (automatic uploads are enabled by configuration)")
	dryRun := flag.Bool("dry-run", false, "inspect one stable snapshot without uploading; implies console mode")
	flag.Parse()
	if err := run(*console || *dryRun, *dryRun); err != nil {
		if *console || *dryRun {
			fmt.Fprintln(os.Stderr, err)
		} else {
			ui.Message("CWG2 Uploader", err.Error())
		}
		os.Exit(1)
	}
}

func run(console, dryRun bool) error {
	release, err := ui.SingleInstance()
	if err != nil {
		return err
	}
	defer release()
	dir, err := ui.DataFolder()
	if err != nil {
		return err
	}
	defaultFolder, err := ui.DefaultSaveFolder()
	if err != nil {
		return err
	}
	config, firstRun, err := storage.LoadConfig(dir, defaultFolder)
	if err != nil {
		return err
	}
	httpClient, err := leaderboard.NewHTTPClient(config.ProxyURL)
	if err != nil {
		return err
	}
	defer httpClient.CloseIdleConnections()
	writer, err := storage.OpenLog(dir)
	if err != nil {
		return err
	}
	defer writer.Close()
	var output io.Writer = writer
	if console {
		output = io.MultiWriter(writer, os.Stdout)
	}
	logger := log.New(output, "", log.Ldate|log.Ltime)
	if dryRun {
		// Inspect even if automatic uploads are paused; do not persist this override.
		config.Automatic = true
	}
	lastUpload, err := storage.LoadUploadState(dir)
	if err != nil {
		return fmt.Errorf("load state.json (fix or remove it before restarting): %w", err)
	}
	if firstRun && !console {
		ui.Message("CWG2 Uploader", "This app automatically sends your CWG2 saves to the third-party public leaderboard. Existing saves will upload on startup. It never modifies game saves.\n\nUse the tray menu to turn off automatic uploads or choose a different save folder. Already queued uploads and retries will continue.\n\nConfiguration and rotating logs: "+dir)
	}
	if !dryRun {
		if err := ui.SetStartup(config.LaunchAtLogin); err != nil {
			log.New(output, "[main] ", logger.Flags()).Printf("Startup registration: %v", err)
		}
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	application := &app.App{
		Dir: dir, Config: config, LastUpload: lastUpload, DryRun: dryRun,
		Client: &leaderboard.Client{URL: leaderboard.BaseURL, HTTP: httpClient},
		Logger: logger, Commands: make(chan app.Command, 16), Updates: make(chan app.Status, 1), SetStartup: ui.SetStartup,
	}
	done := make(chan struct{})
	go func() { defer close(done); application.Run(ctx) }()
	if console {
		for {
			select {
			case <-ctx.Done():
				<-done
				return nil
			case status := <-application.Updates:
				if dryRun && strings.HasPrefix(status.Message, "Dry run:") {
					cancel()
					<-done
					return nil
				}
			}
		}
	}
	ui.RunTray(ctx, cancel, application.Commands, application.Updates, config, dir)
	cancel()
	<-done
	return nil
}
