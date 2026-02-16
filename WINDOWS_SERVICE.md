# Windows Service Installation Guide

Askflow now supports running as a Windows service with automatic startup.

## Installation Methods

### Method 1: Using the Installer (Recommended)

1. Run `build\build_installer.cmd` to create the installer
2. Run the generated `build\installer\askflow-installer.exe`
3. Follow the installation wizard
4. The service will be installed and started automatically

### Method 2: Manual Installation

#### Install the Service

```cmd
askflow.exe install --datadir="C:\ProgramData\Askflow\data"
```

Options:
- `--datadir=<path>`: Specify custom data directory (default: `./data`)

#### Start the Service

```cmd
askflow.exe start
```

Or use Windows Services Manager (`services.msc`) to start "Askflow Support Service"

#### Stop the Service

```cmd
askflow.exe stop
```

#### Uninstall the Service

```cmd
askflow.exe stop
askflow.exe remove
```

## Service Configuration

### Data Directory

The data directory stores:
- Configuration (`config.json`)
- Database files (SQLite)
- Uploaded documents
- Log files
- Images and videos

You can specify a custom data directory during installation or when manually installing the service.

### Logging

When running as a service, Askflow logs to:
1. **Windows Event Log**: Application log with source "AskflowService"
2. **File Log**: `<datadir>\logs\askflow.log`

View Windows event logs:
```cmd
eventvwr.msc
```

View file logs:
```cmd
type C:\ProgramData\Askflow\data\logs\askflow.log
```

### Port Configuration

The service listens on port 8080 by default. To change:
1. Edit `<datadir>\config.json`
2. Restart the service:
   ```cmd
   askflow stop
   askflow start
   ```

## Console Mode

You can still run Askflow in console mode (not as a service):

```cmd
askflow.exe
```

Or with custom data directory:

```cmd
askflow.exe --datadir="C:\CustomPath\data"
```

Console mode is useful for:
- Development and testing
- Running on user login (not system startup)
- Viewing real-time logs in the console

## CLI Commands

All CLI commands work regardless of service status:

```cmd
# Import documents
askflow.exe import --product <id> C:\Docs

# List products
askflow.exe products

# Backup database
askflow.exe backup --output C:\Backups

# Restore from backup
askflow.exe restore C:\Backups\askflow_full_*.tar.gz
```

## Troubleshooting

### Service Won't Start

1. Check Windows Event Log for errors:
   ```cmd
   eventvwr.msc
   ```

2. Check file log:
   ```cmd
   type C:\ProgramData\Askflow\data\logs\askflow.log
   ```

3. Verify data directory exists and is writable

4. Try running in console mode to see errors:
   ```cmd
   askflow.exe --datadir="C:\ProgramData\Askflow\data"
   ```

### Access Denied Errors

The service runs as LocalSystem by default and has full access.

If using a custom data directory, ensure the service account has read/write permissions.

### Port Already in Use

If port 8080 is already in use:
1. Stop the conflicting service
2. Or change Askflow port in `config.json`

### Cannot Install Service

Ensure you're running as Administrator:
- Right-click `cmd.exe` â†?"Run as administrator"
- Then run the install command

## Automatic Startup

When installed as a service, Askflow starts automatically on system boot.

To disable automatic startup:
1. Open `services.msc`
2. Find "Askflow Support Service"
3. Right-click â†?Properties
4. Change "Startup type" to "Manual" or "Disabled"

## Uninstalling

### Using the Installer

1. Open "Add or Remove Programs"
2. Find "Askflow Support Service"
3. Click "Uninstall"
4. Choose whether to keep or delete data directory

### Manual Uninstall

```cmd
askflow.exe stop
askflow.exe remove
```

Then manually delete:
- Installation directory (e.g., `C:\Program Files\Askflow`)
- Data directory (e.g., `C:\ProgramData\Askflow\data`)

## Building the Installer

Requirements:
- Go 1.18 or later
- NSIS 3.0 or later (https://nsis.sourceforge.io/)

Build command:
```cmd
cd D:\workprj\VantageSelfservice
build\build_installer.cmd
```

The installer will be created at:
```
build\installer\askflow-installer.exe
```

## Architecture

The Windows service implementation consists of:

- `internal/service/app_service.go` - Application service layer
- `internal/svc/service.go` - Windows service implementation
- `internal/svc/logger.go` - Dual logging (event log + file log)
- `main.go` - Command dispatcher and service entry point

The service runs the HTTP server in the background and handles Windows service control commands (start, stop, shutdown).
