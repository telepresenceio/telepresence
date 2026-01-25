#!/bin/bash
set -e

# Stop the service if running
if systemctl is-active --quiet telepresence-rootd.service 2>/dev/null; then
    echo "Stopping telepresence-rootd service..."
    systemctl stop telepresence-rootd.service || true
fi

# Disable the service
if systemctl is-enabled --quiet telepresence-rootd.service 2>/dev/null; then
    echo "Disabling telepresence-rootd service..."
    systemctl disable telepresence-rootd.service || true
fi