//go:build windows

package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"helpdesk/internal/service"
	helpdeskSvc "helpdesk/internal/svc"

	"golang.org/x/sys/windows/svc"
)

// isWindowsService checks if running as Windows service
func isWindowsService() bool {
	isService, err := svc.IsWindowsService()
	if err != nil {
		log.Fatalf("Failed to determine if running as service: %v", err)
	}
	return isService
}

// handleInstall installs the Windows service.
func handleInstall(args []string) {
	dataDir := parseDataDirFlag()
	exePath, err := os.Executable()
	if err != nil {
		log.Fatalf("Failed to get executable path: %v", err)
	}

	// Build service startup arguments
	var serviceArgs []string
	if dataDir != "./data" {
		serviceArgs = append(serviceArgs, "--datadir="+dataDir)
	}

	err = helpdeskSvc.InstallService(serviceName, displayName, description, exePath, serviceArgs)
	if err != nil {
		log.Fatalf("Failed to install service: %v", err)
	}

	fmt.Println("✓ Service installed successfully")
	if dataDir != "./data" {
		fmt.Printf("  Data directory: %s\n", dataDir)
	}
	fmt.Println("\nTo start the service, run:")
	fmt.Println("  helpdesk start")
	fmt.Println("\nOr use Windows Services Manager (services.msc)")
}

// handleRemove uninstalls the Windows service.
func handleRemove() {
	err := helpdeskSvc.RemoveService(serviceName)
	if err != nil {
		log.Fatalf("Failed to remove service: %v", err)
	}
	fmt.Println("✓ Service removed successfully")
}

// handleStart starts the Windows service.
func handleStart() {
	err := helpdeskSvc.StartService(serviceName)
	if err != nil {
		log.Fatalf("Failed to start service: %v", err)
	}
	fmt.Println("✓ Service started successfully")
}

// handleStop stops the Windows service.
func handleStop() {
	err := helpdeskSvc.StopService(serviceName)
	if err != nil {
		log.Fatalf("Failed to stop service: %v", err)
	}
	fmt.Println("✓ Service stopped successfully")
}

// runAsService runs the application as a Windows service.
func runAsService(dataDir string) {
	// Initialize service logger
	logger, err := helpdeskSvc.NewServiceLogger(serviceName, true, filepath.Join(dataDir, "logs"))
	if err != nil {
		log.Fatalf("Failed to create service logger: %v", err)
	}
	defer logger.Close()

	// Initialize application service
	appSvc := &service.AppService{}
	if err := appSvc.Initialize(dataDir); err != nil {
		logger.Error("Failed to initialize application: %v", err)
		log.Fatalf("Failed to initialize application: %v", err)
	}

	// Create App and register handlers
	app := createApp(appSvc)
	registerAPIHandlers(app)
	http.Handle("/", spaHandler("frontend/dist"))

	// Create Windows service handler
	helpdeskService := helpdeskSvc.NewHelpdeskService(appSvc, logger)

	// Run as Windows service
	logger.Info("Starting Helpdesk service...")
	if err := svc.Run(serviceName, helpdeskService); err != nil {
		logger.Error("Service failed: %v", err)
		log.Fatalf("Service failed: %v", err)
	}
}
