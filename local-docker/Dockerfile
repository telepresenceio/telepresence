# docker build --file local-docker/Dockerfile .
FROM alpine:3.6

RUN apk add --no-cache python3 openssh-client iptables tini conntrack-tools git && \
    pip3 install git+https://github.com/datawire/sshuttle.git@telepresence && \
    apk del git

COPY setup.py /tmp/build/
COPY telepresence /tmp/build/telepresence
COPY local-docker/entrypoint.py /usr/bin/
RUN pip3 install /tmp/build && chmod +x /usr/bin/entrypoint.py
ENTRYPOINT ["/sbin/tini", "-v", "--", "python3", "/usr/bin/entrypoint.py"]
