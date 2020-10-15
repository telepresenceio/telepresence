# Image containing the bare minimum to support running Telepresence github-actions locally
FROM node:buster

RUN apt-get update \
 && apt-get install --no-install-recommends --no-install-suggests -y \
  curl \
  git \
  iptables \
  sudo

# Install kubectl
RUN curl -L -o /usr/bin/kubectl "https://storage.googleapis.com/kubernetes-release/release/$(curl -s https://storage.googleapis.com/kubernetes-release/release/stable.txt)/bin/linux/amd64/kubectl" && \
  chmod +x /usr/bin/kubectl && \
  kubectl version --client

#install docker
RUN curl -fsSL https://get.docker.com | /bin/sh
