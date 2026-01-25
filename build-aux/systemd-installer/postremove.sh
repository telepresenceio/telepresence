#!/bin/bash
set -e

# Reload systemd to forget about the removed service
systemctl daemon-reload || true

echo ""
echo "Telepresence has been removed."
echo ""
echo "Note: Configuration in /etc/telepresence and logs in /var/log/telepresence"
echo "have been preserved. Remove them manually if no longer needed."
echo ""