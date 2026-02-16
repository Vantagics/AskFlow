//go:build !windows

// Package svc provides logging support for non-Windows platforms.
package svc

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
)

// ServiceLogger provides file-based logging for non-Windows platforms.
type ServiceLogger struct {
	fileLogger *log.Logger // File log
	file       *os.File    // Log file handle
	isService  bool
}

// NewServiceLogger creates a new service logger.
func NewServiceLogger(serviceName string, isService bool, logDir string) (*ServiceLogger, error) {
	sl := &ServiceLogger{isService: isService}

	// Configure file logging
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create log directory: %w", err)
	}

	logPath := filepath.Join(logDir, "askflow.log")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		return nil, fmt.Errorf("failed to open log file: %w", err)
	}
	sl.file = f
	sl.fileLogger = log.New(f, "", log.LstdFlags)

	return sl, nil
}

// Info logs an informational message.
func (sl *ServiceLogger) Info(msg string, args ...interface{}) {
	formattedMsg := fmt.Sprintf(msg, args...)
	if sl.fileLogger != nil {
		sl.fileLogger.Printf("[INFO] %s", formattedMsg)
	}
}

// Error logs an error message.
func (sl *ServiceLogger) Error(msg string, args ...interface{}) {
	formattedMsg := fmt.Sprintf(msg, args...)
	if sl.fileLogger != nil {
		sl.fileLogger.Printf("[ERROR] %s", formattedMsg)
	}
}

// Warning logs a warning message.
func (sl *ServiceLogger) Warning(msg string, args ...interface{}) {
	formattedMsg := fmt.Sprintf(msg, args...)
	if sl.fileLogger != nil {
		sl.fileLogger.Printf("[WARNING] %s", formattedMsg)
	}
}

// Close closes the logger and releases resources.
func (sl *ServiceLogger) Close() {
	if sl.file != nil {
		sl.file.Close()
	}
}
