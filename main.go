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
	"syscall"

	"cwg2uploader/internal/app"
	"cwg2uploader/internal/leaderboard"
	"cwg2uploader/internal/storage"
	ui "cwg2uploader/internal/windows"
)

func main() {
	dryRun := flag.Bool("dry-run", false, "inspect one stable snapshot without uploading; use the console binary for terminal output")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s [--dry-run]\n", os.Args[0])
		if consoleBuild {
			fmt.Fprintln(flag.CommandLine.Output(), "Console build: normal runs upload according to configuration, without the tray.")
		} else {
			fmt.Fprintln(flag.CommandLine.Output(), "Tray build: normal runs use the Windows notification area.")
		}
		flag.PrintDefaults()
	}
	flag.Parse()
	console := consoleBuild || *dryRun
	if err := run(console, *dryRun); err != nil {
		if console {
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
	config, _, err := storage.LoadConfig(dir, ui.DefaultSaveFolder)
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
	lastUpload, err := storage.LoadUploadState(dir)
	if err != nil {
		return fmt.Errorf("load state.json (fix or remove it before restarting): %w", err)
	}
	showNotice := func(message string) error {
		if console {
			_, err := fmt.Fprintln(os.Stdout, message)
			return err
		}
		return ui.Message("CWG2 Uploader", message)
	}
	if err := ensureUploadNotice(dir, &config, dryRun, showNotice); err != nil {
		return err
	}
	if dryRun {
		// Inspect even if automatic uploads are paused; do not persist this override.
		config.Automatic = true
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	application := &app.App{
		Dir: dir, Config: config, LastUpload: lastUpload, DryRun: dryRun,
		Client: &leaderboard.Client{URL: leaderboard.BaseURL, HTTP: httpClient},
		Logger: logger, Commands: make(chan app.Command, 16), Updates: make(chan app.Status, 1),
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
	err = ui.RunTray(ctx, cancel, application.Commands, application.Updates, config, dir)
	cancel()
	<-done
	return err
}

func ensureUploadNotice(dir string, config *storage.Config, dryRun bool, show func(string) error) error {
	if config.UploadNoticeShown {
		return nil
	}
	message := "This app sends CWG2 saves to a third-party public leaderboard. Existing saves upload when Automatic uploads is enabled. It never modifies game saves.\n\nYou can change Automatic uploads and the save folder through the tray or config.json. Already queued uploads and retries continue when Automatic uploads is off."
	message += fmt.Sprintf("\n\nAutomatic uploads configured: %t\nConfiguration and rotating logs: %s", config.Automatic, dir)
	if dryRun {
		message = "Dry run: no saves will be uploaded. This notice will appear again before the first normal run.\n\n" + message
	}
	if err := show(message); err != nil {
		return fmt.Errorf("show upload notice: %w", err)
	}
	if dryRun {
		return nil
	}
	next := *config
	next.UploadNoticeShown = true
	if err := storage.SaveConfig(dir, next); err != nil {
		return fmt.Errorf("save upload notice acknowledgement: %w", err)
	}
	*config = next
	return nil
}
