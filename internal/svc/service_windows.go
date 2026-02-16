//go:build windows

// Package svc provides Windows service support for the Askflow application.
package svc

import (
	"context"
	"fmt"
	"time"

	"askflow/internal/service"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// AskflowService implements the Windows service interface.
type AskflowService struct {
	appService *service.AppService
	logger     *ServiceLogger
}

// NewAskflowService creates a new Windows service instance.
func NewAskflowService(appService *service.AppService, logger *ServiceLogger) *AskflowService {
	return &AskflowService{
		appService: appService,
		logger:     logger,
	}
}

// Execute implements the windows/svc.Handler interface.
// This is called by the Windows Service Control Manager.
func (hs *AskflowService) Execute(args []string, r <-chan svc.ChangeRequest, s chan<- svc.Status) (bool, uint32) {
	// Report service status as Running
	s <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	hs.logger.Info("Askflow service started")

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
				cancel() // Cancel context — Run() will call Shutdown internally
				// Wait for Run() to finish instead of calling Shutdown again
				<-errCh
				s <- svc.Status{State: svc.Stopped}
				hs.logger.Info("Askflow service stopped")
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
