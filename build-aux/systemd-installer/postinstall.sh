#!/bin/bash
set -e

# Create required directories
mkdir -p /var/cache/telepresence/rootd
mkdir -p /var/log/telepresence
mkdir -p /etc/telepresence
chmod 755 /var/cache/telepresence
chmod 755 /var/cache/telepresence/rootd
chmod 755 /var/log/telepresence

# Reload systemd to pick up the new service file (may fail in containers)
systemctl daemon-reload 2>/dev/null || true

# Enable and start the service
systemctl enable telepresence-rootd.service 2>/dev/null || true
systemctl start telepresence-rootd.service 2>/dev/null || true

echo ""
echo "Telepresence has been installed successfully!"
echo ""
echo "The root daemon service has been enabled and started."
echo ""
echo "To check service status:"
echo "  sudo systemctl status telepresence-rootd"
echo ""
echo "Service logs are written to /var/log/telepresence/rootd.log"
echo ""