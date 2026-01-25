#!/bin/bash
set -e

echo "Uninstalling Telepresence..."

# Stop and disable the service
if systemctl is-active --quiet telepresence-rootd.service 2>/dev/null; then
    echo "Stopping telepresence-rootd service..."
    sudo systemctl stop telepresence-rootd.service
fi

if systemctl is-enabled --quiet telepresence-rootd.service 2>/dev/null; then
    echo "Disabling telepresence-rootd service..."
    sudo systemctl disable telepresence-rootd.service
fi

# Remove files
echo "Removing files..."
sudo rm -f /usr/local/bin/telepresence
sudo rm -f /usr/local/bin/telepresence-uninstall
sudo rm -f /usr/lib/systemd/system/telepresence-rootd.service

# Reload systemd
sudo systemctl daemon-reload

echo ""
echo "Telepresence has been uninstalled."
echo ""
echo "The following directories have been preserved:"
echo "  /etc/telepresence     - configuration"
echo "  /var/cache/telepresence - cache data"
echo "  /var/log/telepresence - logs"
echo ""
echo "Remove them manually if no longer needed:"
echo "  sudo rm -rf /etc/telepresence /var/cache/telepresence /var/log/telepresence"
echo ""