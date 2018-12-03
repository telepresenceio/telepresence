#!/usr/bin/env sh
set -e
/usr/sbin/sshd -e
exec env PYTHONPATH=/usr/src/app twistd -n -y ./forwarder.py
