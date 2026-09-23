#!/bin/bash
set -e

# rpm passes 0 for a removal and 1 for an upgrade; dpkg passes the action.
case "${1:-}" in
    0|remove|purge|deconfigure) ;;
    *) exit 0 ;;
esac

if systemctl is-active --quiet telepresence-rootd.service 2>/dev/null; then
    echo "Stopping telepresence-rootd service..."
    systemctl stop telepresence-rootd.service || true
fi

if systemctl is-enabled --quiet telepresence-rootd.service 2>/dev/null; then
    echo "Disabling telepresence-rootd service..."
    systemctl disable telepresence-rootd.service || true
fi
