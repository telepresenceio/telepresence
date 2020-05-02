#!/usr/bin/env sh
set -e
echo -e ",s/1000/`id -u`/g\\012 w" | ed -s /etc/passwd
ssh-keygen -A
/usr/sbin/sshd -e
if [ "$TELEPRESENCE_SUPPRESS_PROXY_OUTPUT" = "1" ]; then
    exec env PYTHONPATH=/usr/src/app twistd -l=- --pidfile= -n -y ./forwarder.py 2>&1 >/dev/null
else
    exec env PYTHONPATH=/usr/src/app twistd --pidfile= -n -y ./forwarder.py
fi;