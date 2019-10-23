#!/usr/bin/env sh
set -e
echo -e ",s/1000/`id -u`/g\\012 w" | ed -s /etc/passwd
ssh-keygen -A
/usr/sbin/sshd -e
exec env PYTHONPATH=/usr/src/app twistd --pidfile= -n -y ./forwarder.py
