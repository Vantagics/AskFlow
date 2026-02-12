//go:build !windows

package main

import (
	"fmt"
	"log"
)

// isWindowsService always returns false on non-Windows platforms
func isWindowsService() bool {
	return false
}

// handleInstall is not supported on non-Windows platforms.
func handleInstall(args []string) {
	fmt.Println("Windows service installation is not supported on this platform")
	fmt.Println("Use systemd or your system's service manager instead")
	log.Fatal("Unsupported operation")
}

// handleRemove is not supported on non-Windows platforms.
func handleRemove() {
	fmt.Println("Windows service removal is not supported on this platform")
	log.Fatal("Unsupported operation")
}

// handleStart is not supported on non-Windows platforms.
func handleStart() {
	fmt.Println("Windows service start is not supported on this platform")
	log.Fatal("Unsupported operation")
}

// handleStop is not supported on non-Windows platforms.
func handleStop() {
	fmt.Println("Windows service stop is not supported on this platform")
	log.Fatal("Unsupported operation")
}

// runAsService is not supported on non-Windows platforms.
func runAsService(dataDir string) {
	fmt.Println("Windows service mode is not supported on this platform")
	log.Fatal("Unsupported operation")
}
