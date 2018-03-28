# docker build --file Dockerfile.releaser -t datawire/telepresence-releaser .
FROM alpine:3.7

WORKDIR /root
RUN apk --no-cache add bash build-base ca-certificates curl git openssh-client py-pip ruby ruby-dev && \
    pip install awscli && \
    gem install --no-document package_cloud && \
    git config --global user.email "services@datawire.io" && \
    git config --global user.name "d6e automaton"

ENTRYPOINT [ "/bin/bash" ]
