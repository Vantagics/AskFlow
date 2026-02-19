//go:build windows

package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"askflow/internal/handler"
	"askflow/internal/router"
	"askflow/internal/service"
	askflowSvc "askflow/internal/svc"

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
	bind := parseBindFlag()
	port := parsePortFlag()
	exePath, err := os.Executable()
	if err != nil {
		log.Fatalf("Failed to get executable path: %v", err)
	}

	// Build service startup arguments
	var serviceArgs []string
	if dataDir != "./data" {
		serviceArgs = append(serviceArgs, "--datadir="+dataDir)
	}
	if bind != "" {
		serviceArgs = append(serviceArgs, "--bind="+bind)
	}
	if port > 0 {
		serviceArgs = append(serviceArgs, fmt.Sprintf("--port=%d", port))
	}

	err = askflowSvc.InstallService(serviceName, displayName, description, exePath, serviceArgs)
	if err != nil {
		log.Fatalf("Failed to install service: %v", err)
	}

	fmt.Println("✓ Service installed successfully")
	if dataDir != "./data" {
		fmt.Printf("  Data directory: %s\n", dataDir)
	}
	if bind != "" {
		fmt.Printf("  Bind address: %s\n", bind)
	}
	if port > 0 {
		fmt.Printf("  Port: %d\n", port)
	}
	fmt.Println("\nTo start the service, run:")
	fmt.Println("  askflow start")
	fmt.Println("\nOr use Windows Services Manager (services.msc)")
}

// handleRemove uninstalls the Windows service.
func handleRemove() {
	err := askflowSvc.RemoveService(serviceName)
	if err != nil {
		log.Fatalf("Failed to remove service: %v", err)
	}
	fmt.Println("�?Service removed successfully")
}

// handleStart starts the Windows service.
func handleStart() {
	err := askflowSvc.StartService(serviceName)
	if err != nil {
		log.Fatalf("Failed to start service: %v", err)
	}
	fmt.Println("�?Service started successfully")
}

// handleStop stops the Windows service.
func handleStop() {
	err := askflowSvc.StopService(serviceName)
	if err != nil {
		log.Fatalf("Failed to stop service: %v", err)
	}
	fmt.Println("�?Service stopped successfully")
}

// runAsService runs the application as a Windows service.
func runAsService(dataDir string) {
	// Initialize service logger
	logger, err := askflowSvc.NewServiceLogger(serviceName, true, filepath.Join(dataDir, "logs"))
	if err != nil {
		log.Fatalf("Failed to create service logger: %v", err)
	}
	defer logger.Close()

	port := parsePortFlag()
	bind := parseBindFlag()

	// Initialize application service
	appSvc := &service.AppService{}
	if err := appSvc.Initialize(dataDir, bind, port); err != nil {
		logger.Error("Failed to initialize application: %v", err)
		log.Fatalf("Failed to initialize application: %v", err)
	}

	// Create App and register handlers
	app := appSvc.CreateApp()
	cleanupRouter := router.Register(app)
	defer cleanupRouter()
	http.Handle("/", handler.SpaHandler("frontend/dist"))

	// Create Windows service handler
	askflowService := askflowSvc.NewAskflowService(appSvc, logger)

	// Run as Windows service
	logger.Info("Starting Askflow service...")
	if err := svc.Run(serviceName, askflowService); err != nil {
		logger.Error("Service failed: %v", err)
		log.Fatalf("Service failed: %v", err)
	}
}
