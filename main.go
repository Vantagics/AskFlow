package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"time"

	"askflow/internal/cli"
	"askflow/internal/handler"
	"askflow/internal/router"
	"askflow/internal/service"
)

const (
	serviceName = "AskflowService"
	displayName = "Askflow Support Service"
	description = "Vantage Askflow RAG Question Answering Service"
)

func main() {
	// Check if running as Windows service
	isService := isWindowsService()

	// Parse datadir flag from command line
	dataDir := parseDataDirFlag()

	// Handle command-line commands
	if len(os.Args) >= 2 && !isService {
		switch os.Args[1] {
		// Windows service management commands
		case "install":
			handleInstall(os.Args[2:])
			return
		case "remove":
			handleRemove()
			return
		case "start":
			handleStart()
			return
		case "stop":
			handleStop()
			return

		// CLI commands
		case "import":
			runCLICommand(dataDir, func(appSvc *service.AppService) {
				cli.RunBatchImport(os.Args[2:], appSvc.GetDocManager(), appSvc.GetProductService())
			})
			return
		case "backup":
			runCLICommand(dataDir, func(appSvc *service.AppService) {
				cli.RunBackup(os.Args[2:], appSvc.GetDatabase())
			})
			return
		case "restore":
			cli.RunRestore(os.Args[2:])
			return
		case "products":
			runCLICommand(dataDir, func(appSvc *service.AppService) {
				cli.RunListProducts(appSvc.GetProductService())
			})
			return
		case "help", "-h", "--help":
			printUsage()
			return
		}
	}

	// Run application
	if isService {
		runAsService(dataDir)
	} else {
		runAsConsoleApp(dataDir)
	}
}

// parseDataDirFlag extracts the --datadir flag from command line arguments.
func parseDataDirFlag() string {
	for i, arg := range os.Args {
		if strings.HasPrefix(arg, "--datadir=") {
			return strings.TrimPrefix(arg, "--datadir=")
		}
		if arg == "--datadir" && i+1 < len(os.Args) {
			return os.Args[i+1]
		}
	}
	return "./data"
}

// parsePortFlag extracts the --port or -p flag from command line arguments.
func parsePortFlag() int {
	for i, arg := range os.Args {
		if strings.HasPrefix(arg, "--port=") {
			port, err := strconv.Atoi(strings.TrimPrefix(arg, "--port="))
			if err == nil {
				return port
			}
		}
		if (arg == "--port" || arg == "-p") && i+1 < len(os.Args) {
			port, err := strconv.Atoi(os.Args[i+1])
			if err == nil {
				return port
			}
		}
	}
	return 0
}

// parseBindFlag extracts the --bind flag or IP version shorthands (-4/-6) from command line arguments.
func parseBindFlag() string {
	// Check --bind first
	for i, arg := range os.Args {
		if strings.HasPrefix(arg, "--bind=") {
			return strings.TrimPrefix(arg, "--bind=")
		}
		if arg == "--bind" && i+1 < len(os.Args) {
			return os.Args[i+1]
		}
	}

	// Check for shorthand IP version flags
	for _, arg := range os.Args {
		if arg == "-4" || arg == "--ipv4" {
			return "0.0.0.0"
		}
		if arg == "-6" || arg == "--ipv6" {
			return "::"
		}
	}
	return ""
}

// runAsConsoleApp runs the application in console mode.
func runAsConsoleApp(dataDir string) {
	bind := parseBindFlag()
	port := parsePortFlag()

	// Initialize application service
	appSvc := &service.AppService{}
	if err := appSvc.Initialize(dataDir, bind, port); err != nil {
		log.Fatalf("Failed to initialize application: %v", err)
	}

	// Create App and register handlers
	app := appSvc.CreateApp()
	cleanupRouter := router.Register(app)
	defer cleanupRouter()
	http.Handle("/", handler.SpaHandler("frontend/dist"))

	// Run with graceful shutdown
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	fmt.Printf("Starting Askflow in console mode (data directory: %s)...\n", dataDir)
	if err := appSvc.Run(ctx); err != nil && err != http.ErrServerClosed {
		log.Fatalf("Server error: %v", err)
	}
}

// runCLICommand initializes the app service and runs a CLI command.
func runCLICommand(dataDir string, fn func(*service.AppService)) {
	appSvc := &service.AppService{}
	if err := appSvc.Initialize(dataDir, "", 0); err != nil {
		log.Fatalf("Failed to initialize application: %v", err)
	}
	defer appSvc.Shutdown(5 * time.Second)
	fn(appSvc)
}

// printUsage prints CLI usage information.
func printUsage() {
	fmt.Println(`Usage:
  askflow                                        Start HTTP service (default port 8080)
  askflow --bind=<addr>                          Specify listen address (e.g., 0.0.0.0, ::, 127.0.0.1)
  askflow -4, --ipv4                             Listen on IPv4 only (equivalent to --bind=0.0.0.0)
  askflow -6, --ipv6                             Listen on IPv6 (equivalent to --bind=::)
  askflow --port=<port>                          Specify service port (or -p <port>)
  askflow --datadir=<path>                       Specify data directory

Windows Service Commands:
  askflow install [-4|-6] [--bind=<addr>] [--port=<port>]  Install as Windows service
  askflow remove                                           Uninstall Windows service
  askflow start                                            Start Windows service
  askflow stop                                             Stop Windows service

CLI Commands:
  askflow import [--product <product_id>] <目录> [...]  批量导入目录下的文档到知识库
  askflow products                                         List all products and their IDs
  askflow backup [options]                                 Backup all system data
  askflow restore <backup_file>                            Restore data from backup
  askflow help                                             Show this help information

import command:
  Recursively scan specified directories and subdirectories for supported files
  (PDF, Word, Excel, PPT, Markdown, HTML), parse them, and store in vector database.
  Multiple directories can be specified.

  Options:
    --product <product_id>  Specify target product ID. Imported documents will be associated
                            with this product. If not specified, they will be imported to the public library.

  Supported formats: .pdf .doc .docx .xls .xlsx .ppt .pptx .md .markdown .html .htm

  Examples:
    askflow import ./docs
    askflow import ./docs ./manuals /path/to/files
    askflow import --product abc123 ./docs

products command:
  List all products' IDs, names, and descriptions in the system.

  Example:
    askflow products

backup command:
  Backup all system data into a tiered tar.gz archive.
  Full mode: Complete database snapshot + all uploaded files + configuration.
  Incremental mode: Export only new database rows + new uploaded files + configuration.

  Backup filename: askflow_<mode>_<hostname>_<date-time>.tar.gz
  Example: askflow_full_myserver_20260212-143000.tar.gz

  Options:
    --output <dir>     Output directory for backup file (default: current directory)
    --incremental      Incremental backup mode
    --base <manifest>  Path to base manifest file (required for incremental mode)

  Examples:
    askflow backup                                    Full backup to current directory
    askflow backup --output ./backups                 Full backup to specified directory
    askflow backup --incremental --base ./backups/askflow_full_myserver_20260212-143000.manifest.json

restore command:
  Restore data from a backup archive to the data directory.
  Full restore: Extract and run directly.
  Incremental restore: Restore full backup first, then apply db_delta.sql from incremental backups.

  Options:
    --target <dir>     Target restore directory (default: ./data)

  Examples:
    askflow restore askflow_full_myserver_20260212-143000.tar.gz
    askflow restore --target ./data-new backup.tar.gz`)
}
