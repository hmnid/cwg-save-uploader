package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"cwg2uploader/internal/storage"
)

func (s *runState) handleCommand(ctx context.Context, command Command) error {
	switch command.Kind {
	case "upload":
		s.app.ForceUploadRequested = true
		// Manual retry does not bypass Retry-After or stability checks.
	case "pause":
		if err := s.toggleAutomatic(); err != nil {
			return err
		}
	case "folder":
		if err := s.changeFolder(command.Value); err != nil {
			return err
		}
	case "startup":
		if err := s.toggleStartup(); err != nil {
			return err
		}
		s.publish(s.lastMessage)
		return nil
	default:
		return nil
	}
	s.tick(ctx, time.Now())
	return nil
}

func (s *runState) toggleAutomatic() error {
	config := s.app.Config
	config.Automatic = !config.Automatic
	return s.saveConfig(config)
}

func (s *runState) changeFolder(path string) error {
	if s.uploader.busy {
		return errors.New("Wait for the current upload before changing folders")
	}
	expanded, err := storage.ExpandPath(path)
	if err != nil {
		return err
	}
	config := s.app.Config
	config.SaveFolder = expanded
	if err := s.saveConfig(config); err != nil {
		return err
	}
	s.resetPoller(expanded)
	s.uploader.pending = nil
	s.uploader.clearRetry()
	return nil
}

func (s *runState) toggleStartup() error {
	if s.app.SetStartup == nil {
		return errors.New("Startup integration unavailable")
	}
	previous := s.app.Config.LaunchAtLogin
	if err := s.app.SetStartup(!previous); err != nil {
		return fmt.Errorf("Startup registration: %w", err)
	}
	config := s.app.Config
	config.LaunchAtLogin = !previous
	if err := s.saveConfig(config); err != nil {
		_ = s.app.SetStartup(previous)
		return err
	}
	return nil
}

func (s *runState) saveConfig(config storage.Config) error {
	if err := storage.SaveConfig(s.app.Dir, config); err != nil {
		return fmt.Errorf("Save config: %w", err)
	}
	s.app.Config = config
	return nil
}
