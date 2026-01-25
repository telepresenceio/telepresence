#!/bin/bash
set -e

# Create required directories
mkdir -p /var/cache/telepresence/rootd
mkdir -p /etc/telepresence
chmod 755 /var/cache/telepresence
chmod 755 /var/cache/telepresence/rootd

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
echo "To view service logs:"
echo "  sudo journalctl -u telepresence-rootd"
echo ""