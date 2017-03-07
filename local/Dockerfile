FROM alpine:3.5

RUN set -e && \
    apk add --no-cache python3 curl ca-certificates openssh-client sshpass && \
    curl https://storage.googleapis.com/kubernetes-release/release/v1.5.3/bin/linux/amd64/kubectl -o /usr/local/bin/kubectl && \
    chmod +x /usr/local/bin/kubectl

ADD ./entrypoint.py /usr/local/bin/entrypoint.py
RUN ["chmod", "+x", "/usr/local/bin/entrypoint.py"]

ENV HOME=/opt

ENTRYPOINT ["/usr/local/bin/entrypoint.py"]
