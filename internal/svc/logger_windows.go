//go:build windows

// Package svc provides Windows service support for the Helpdesk application.
package svc

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows/svc/eventlog"
)

// ServiceLogger provides dual logging: Windows event log + file log.
type ServiceLogger struct {
	eventLogger *eventlog.Log // Windows event log
	fileLogger  *log.Logger   // File log
	file        *os.File      // Log file handle
	isService   bool
}

// NewServiceLogger creates a new service logger.
// If isService is true, logs will be written to both Windows event log and file.
// If isService is false, logs will only be written to file.
func NewServiceLogger(serviceName string, isService bool, logDir string) (*ServiceLogger, error) {
	sl := &ServiceLogger{isService: isService}

	// 1. Always configure file logging
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create log directory: %w", err)
	}

	logPath := filepath.Join(logDir, "helpdesk.log")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		return nil, fmt.Errorf("failed to open log file: %w", err)
	}
	sl.file = f
	sl.fileLogger = log.New(f, "", log.LstdFlags)

	// 2. If running as service, also configure Windows event log
	if isService {
		el, err := eventlog.Open(serviceName)
		if err != nil {
			// Try to create event log source (requires admin rights)
			err = eventlog.InstallAsEventCreate(serviceName,
				eventlog.Info|eventlog.Warning|eventlog.Error)
			if err == nil {
				el, err = eventlog.Open(serviceName)
			}
		}
		if err != nil {
			sl.fileLogger.Printf("WARNING: Failed to open Windows event log: %v", err)
			// Continue without event log - file log is sufficient
		} else {
			sl.eventLogger = el
		}
	}

	return sl, nil
}

// Info logs an informational message.
func (sl *ServiceLogger) Info(msg string, args ...interface{}) {
	formattedMsg := fmt.Sprintf(msg, args...)

	// Write to file log
	if sl.fileLogger != nil {
		sl.fileLogger.Printf("[INFO] %s", formattedMsg)
	}

	// Write to event log if available
	if sl.isService && sl.eventLogger != nil {
		sl.eventLogger.Info(1, formattedMsg)
	}
}

// Error logs an error message.
func (sl *ServiceLogger) Error(msg string, args ...interface{}) {
	formattedMsg := fmt.Sprintf(msg, args...)

	// Write to file log
	if sl.fileLogger != nil {
		sl.fileLogger.Printf("[ERROR] %s", formattedMsg)
	}

	// Write to event log if available
	if sl.isService && sl.eventLogger != nil {
		sl.eventLogger.Error(1, formattedMsg)
	}
}

// Warning logs a warning message.
func (sl *ServiceLogger) Warning(msg string, args ...interface{}) {
	formattedMsg := fmt.Sprintf(msg, args...)

	// Write to file log
	if sl.fileLogger != nil {
		sl.fileLogger.Printf("[WARNING] %s", formattedMsg)
	}

	// Write to event log if available
	if sl.isService && sl.eventLogger != nil {
		sl.eventLogger.Warning(1, formattedMsg)
	}
}

// Close closes the logger and releases resources.
func (sl *ServiceLogger) Close() {
	if sl.eventLogger != nil {
		sl.eventLogger.Close()
	}
	if sl.file != nil {
		sl.file.Close()
	}
}
