#!/bin/bash
cd /root/vantageselfservice

# --- Migration from helpdesk to askflow ---

# Rename database file
if [ -f data/helpdesk.db ] && [ ! -f data/askflow.db ]; then
    echo "Migrating data/helpdesk.db -> data/askflow.db ..."
    mv data/helpdesk.db data/askflow.db
    # Also migrate WAL/SHM if present
    [ -f data/helpdesk.db-wal ] && mv data/helpdesk.db-wal data/askflow.db-wal
    [ -f data/helpdesk.db-shm ] && mv data/helpdesk.db-shm data/askflow.db-shm
fi

# Rename log file
if [ -f data/logs/helpdesk.log ]; then
    mv data/logs/helpdesk.log data/logs/askflow.log
fi

# Migrate encryption key env var
if [ -n "$HELPDESK_ENCRYPTION_KEY" ] && [ -z "$ASKFLOW_ENCRYPTION_KEY" ]; then
    export ASKFLOW_ENCRYPTION_KEY="$HELPDESK_ENCRYPTION_KEY"
fi

# Kill old process name if still running
pkill -f './helpdesk' 2>/dev/null

# --- Start askflow ---

pkill -f './askflow' 2>/dev/null
sleep 1
nohup ./askflow > askflow.log 2>&1 &
sleep 2
if ss -tlnp | grep -q ':8080 '; then
    echo SERVICE_OK
else
    # Check if config corruption caused the failure
    if grep -q "cipher: message authentication failed" askflow.log 2>/dev/null; then
        echo "Config file corrupted, removing and retrying..."
        rm -f data/config.json
        pkill -f './askflow' 2>/dev/null
        sleep 1
        nohup ./askflow > askflow.log 2>&1 &
        sleep 2
        if ss -tlnp | grep -q ':8080 '; then
            echo SERVICE_OK
        else
            echo SERVICE_FAIL
            tail -5 askflow.log
        fi
    else
        echo SERVICE_FAIL
        tail -5 askflow.log
    fi
fi
