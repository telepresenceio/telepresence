FROM alpine:3.5

RUN mkdir -p /usr/src/app
WORKDIR /usr/src/app
COPY requirements.txt /usr/src/app

# For sshd we set timeouts (ClientAliveInterval), allow root to login with empty
# password, and allow proxying. We then set an empty password for root. For some
# reason pip doesn't install incremental (a Twisted dependency) so do so
# manually. When done, remove unneeded packages for a smaller image.
RUN apk add --no-cache python3 python3-dev openssh gcc libc-dev && \
    ssh-keygen -A && \
    echo -e "ClientAliveInterval 1\nGatewayPorts yes\nPermitRootLogin yes\nPermitEmptyPasswords yes\n" >> /etc/ssh/sshd_config && \
    passwd -d root && \
    pip3 install --no-cache-dir incremental && \
    pip3 install --no-cache-dir -r requirements.txt && \
    apk del -r gcc libc-dev python3-dev

COPY forwarder.py /usr/src/app
COPY socks.py /usr/src/app
COPY add-volume-export /bin
RUN chmod +x /bin/add-volume-export
COPY . /usr/src/app
RUN mkdir -p /export/telepresence-root

# This is set on the remote pod, both for information purposes but also for use
# by some of the end-to-end tests. Don't delete this without modifying
# corresponding test:
ENV TELEPRESENCE_PROXY=1

CMD /usr/sbin/sshd -f /etc/ssh/sshd_config && \
    env PYTHONPATH=/usr/src/app twistd -n -y ./forwarder.py
