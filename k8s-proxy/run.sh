#!/usr/bin/env sh

USER_ID="$(id -u)"
GROUP_ID="$(id -g)"

# This is a terrible hack to allow SSH login to a runtime-specified UID
echo "telepresence::${USER_ID}:${GROUP_ID}:Telepresence User:/usr/src/app:/bin/ash" >> /etc/passwd

#/usr/sbin/sshd -E /usr/src/app/sshd.log
dropbear -R -B -E -a -p 8022
exec env PYTHONPATH=/usr/src/app twistd -n -y ./forwarder.py
