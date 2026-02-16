//go:build !windows

// Package svc provides stub implementations for non-Windows platforms.
package svc

import (
	"fmt"

	"askflow/internal/service"
)

// AskflowService is a stub for non-Windows platforms.
type AskflowService struct {
	appService *service.AppService
	logger     *ServiceLogger
}

// NewAskflowService creates a stub service instance.
func NewAskflowService(appService *service.AppService, logger *ServiceLogger) *AskflowService {
	return &AskflowService{
		appService: appService,
		logger:     logger,
	}
}

// InstallService is not supported on non-Windows platforms.
func InstallService(name, displayName, description, exePath string, serviceArgs []string) error {
	return fmt.Errorf("Windows service installation is not supported on this platform")
}

// RemoveService is not supported on non-Windows platforms.
func RemoveService(name string) error {
	return fmt.Errorf("Windows service removal is not supported on this platform")
}

// StartService is not supported on non-Windows platforms.
func StartService(name string) error {
	return fmt.Errorf("Windows service start is not supported on this platform")
}

// StopService is not supported on non-Windows platforms.
func StopService(name string) error {
	return fmt.Errorf("Windows service stop is not supported on this platform")
}
