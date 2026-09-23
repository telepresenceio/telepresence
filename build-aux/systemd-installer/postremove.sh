#!/bin/bash
set -e

# Reload systemd to forget about the removed service
systemctl daemon-reload 2>/dev/null || true

# rpm passes 0 for a removal and 1 for an upgrade; dpkg passes the action.
case "${1:-}" in
    0|remove|purge) ;;
    *) exit 0 ;;
esac

echo ""
echo "Telepresence has been removed."
echo ""
echo "Note: Configuration in /etc/telepresence and logs in /var/log/telepresence"
echo "have been preserved. Remove them manually if no longer needed."
echo ""
