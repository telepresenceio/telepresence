#!/bin/bash
set -e

# Runs after every scriptlet of an rpm transaction, including the removed
# package's, so an upgrade always ends with the new daemon enabled and running.
systemctl daemon-reload 2>/dev/null || true
systemctl enable telepresence-rootd.service 2>/dev/null || true
if systemctl is-active --quiet telepresence-rootd.service 2>/dev/null; then
    systemctl restart telepresence-rootd.service 2>/dev/null || true
else
    systemctl start telepresence-rootd.service 2>/dev/null || true
fi
