//go:build windows

// Package svc provides Windows service support for the Helpdesk application.
package svc

import (
	"context"
	"fmt"
	"time"

	"helpdesk/internal/service"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// HelpdeskService implements the Windows service interface.
type HelpdeskService struct {
	appService *service.AppService
	logger     *ServiceLogger
}

// NewHelpdeskService creates a new Windows service instance.
func NewHelpdeskService(appService *service.AppService, logger *ServiceLogger) *HelpdeskService {
	return &HelpdeskService{
		appService: appService,
		logger:     logger,
	}
}

// Execute implements the windows/svc.Handler interface.
// This is called by the Windows Service Control Manager.
func (hs *HelpdeskService) Execute(args []string, r <-chan svc.ChangeRequest, s chan<- svc.Status) (bool, uint32) {
	// Report service status as Running
	s <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	hs.logger.Info("Helpdesk service started")

	// Start the application service in a goroutine
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- hs.appService.Run(ctx)
	}()

	// Monitor service control commands
	for {
		select {
		case c := <-r:
			switch c.Cmd {
			case svc.Stop, svc.Shutdown:
				hs.logger.Info("Received stop/shutdown command")
				s <- svc.Status{State: svc.StopPending}
				cancel() // Cancel context to trigger graceful shutdown
				hs.appService.Shutdown(30 * time.Second)
				s <- svc.Status{State: svc.Stopped}
				hs.logger.Info("Helpdesk service stopped")
				return false, 0

			case svc.Interrogate:
				s <- c.CurrentStatus

			default:
				hs.logger.Warning("Unexpected service control command: %v", c.Cmd)
			}

		case err := <-errCh:
			if err != nil {
				hs.logger.Error("Application error: %v", err)
				s <- svc.Status{State: svc.Stopped}
				return false, 1
			}
			// Normal exit
			s <- svc.Status{State: svc.Stopped}
			return false, 0
		}
	}
}

// InstallService installs the Windows service.
func InstallService(name, displayName, description, exePath string, serviceArgs []string) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("failed to connect to service manager: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(name)
	if err == nil {
		s.Close()
		return fmt.Errorf("service %s already exists", name)
	}

	s, err = m.CreateService(name, exePath, mgr.Config{
		DisplayName: displayName,
		Description: description,
		StartType:   mgr.StartAutomatic,
	}, serviceArgs...)
	if err != nil {
		return fmt.Errorf("failed to create service: %w", err)
	}
	defer s.Close()

	return nil
}

// RemoveService uninstalls the Windows service.
func RemoveService(name string) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("failed to connect to service manager: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(name)
	if err != nil {
		return fmt.Errorf("service %s not found: %w", name, err)
	}
	defer s.Close()

	err = s.Delete()
	if err != nil {
		return fmt.Errorf("failed to delete service: %w", err)
	}

	return nil
}

// StartService starts the Windows service.
func StartService(name string) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("failed to connect to service manager: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(name)
	if err != nil {
		return fmt.Errorf("service %s not found: %w", name, err)
	}
	defer s.Close()

	err = s.Start()
	if err != nil {
		return fmt.Errorf("failed to start service: %w", err)
	}

	return nil
}

// StopService stops the Windows service.
func StopService(name string) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("failed to connect to service manager: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(name)
	if err != nil {
		return fmt.Errorf("service %s not found: %w", name, err)
	}
	defer s.Close()

	status, err := s.Control(svc.Stop)
	if err != nil {
		return fmt.Errorf("failed to stop service: %w", err)
	}

	// Wait for service to stop (up to 30 seconds)
	timeout := time.Now().Add(30 * time.Second)
	for status.State != svc.Stopped {
		if time.Now().After(timeout) {
			return fmt.Errorf("timeout waiting for service to stop")
		}
		time.Sleep(300 * time.Millisecond)
		status, err = s.Query()
		if err != nil {
			return fmt.Errorf("failed to query service status: %w", err)
		}
	}

	return nil
}
