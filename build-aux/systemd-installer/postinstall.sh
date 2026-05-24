#!/bin/bash
set -e

# Create required directories
mkdir -p /var/cache/telepresence/rootd
mkdir -p /etc/telepresence
chmod 755 /var/cache/telepresence
chmod 755 /var/cache/telepresence/rootd
chmod 755 /etc/telepresence

# Linux package installs are non-interactive. Opting out at install time is
# done by exporting TELEPRESENCE_USAGE_OPT_OUT=1 before running the package
# manager (works for scripted deploys); otherwise admins can drop the
# marker file by hand after install. The daemon treats the marker's
# presence as an unconditional opt-out, overriding any usage.enabled in
# /etc/telepresence/config.yml.
MARKER=/etc/telepresence/usage-opt-out
case "${TELEPRESENCE_USAGE_OPT_OUT:-}" in
    1|true|TRUE|yes|YES)
        : > "$MARKER"
        chmod 644 "$MARKER"
        echo "Anonymous usage reporting disabled via $MARKER"
        ;;
esac

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
echo "Telepresence is an open-source project without conventional customers."
echo "Anonymous usage data is how we learn what to prioritize — without it"
echo "the community is flying blind. Reports contain only an installation"
echo "UUID, OS/arch, version, and command topics; never cluster, namespace,"
echo "workload, or address data."
echo ""
if [ -e "$MARKER" ]; then
    echo "Reporting is currently DISABLED (marker file at $MARKER)."
    echo "To opt back in for all users on this host, remove the marker:"
    echo "  sudo rm $MARKER"
else
    echo "Reporting is enabled. To opt out for all users on this host:"
    echo "  sudo touch $MARKER"
    echo "(or pass TELEPRESENCE_USAGE_OPT_OUT=1 to future package installs)."
fi
echo ""
echo "To check service status:"
echo "  sudo systemctl status telepresence-rootd"
echo ""
echo "To view service logs:"
echo "  sudo journalctl -u telepresence-rootd"
echo ""
