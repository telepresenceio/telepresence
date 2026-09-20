#!/usr/bin/env bash
# In-VM provisioning for a regression-shard VM: packages, kubectl,
# minikube, and the Go toolchain. Runs once per VM lifetime, as root.
# minikube itself is started by run-shard.sh, not here.
set -euo pipefail

KUBECTL_VERSION="v1.33.5"
MINIKUBE_VERSION="v1.36.0"
HELM_VERSION="v3.19.0"
GO_VERSION="1.26.3"
ARCH="amd64"

export DEBIAN_FRONTEND=noninteractive

apt-get update
apt-get install -y --no-install-recommends \
    ca-certificates \
    conntrack \
    curl \
    docker-compose-v2 \
    docker.io \
    gcc \
    git \
    jq \
    libc6-dev \
    libfuse-dev \
    make \
    rsync \
    socat \
    sshfs

grep -q '^user_allow_other$' /etc/fuse.conf || echo 'user_allow_other' >>/etc/fuse.conf

usermod -aG docker vagrant

curl -fsSL -o /usr/local/bin/kubectl \
    "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${ARCH}/kubectl"
chmod +x /usr/local/bin/kubectl

curl -fsSL -o /usr/local/bin/minikube \
    "https://storage.googleapis.com/minikube/releases/${MINIKUBE_VERSION}/minikube-linux-${ARCH}"
chmod +x /usr/local/bin/minikube

curl -fsSL -o /tmp/helm.tar.gz \
    "https://get.helm.sh/helm-${HELM_VERSION}-linux-${ARCH}.tar.gz"
tar -C /tmp -xzf /tmp/helm.tar.gz "linux-${ARCH}/helm"
install -m 755 "/tmp/linux-${ARCH}/helm" /usr/local/bin/helm
rm -rf /tmp/helm.tar.gz "/tmp/linux-${ARCH}"

curl -fsSL -o /tmp/go.tar.gz \
    "https://go.dev/dl/go${GO_VERSION}.linux-${ARCH}.tar.gz"
rm -rf /usr/local/go
tar -C /usr/local -xzf /tmp/go.tar.gz
rm -f /tmp/go.tar.gz

cat > /etc/profile.d/golang.sh <<'EOF'
export PATH="/usr/local/go/bin:$HOME/go/bin:$PATH"
EOF
chmod 644 /etc/profile.d/golang.sh

# The kubelet consumes inotify instances in proportion to the pods it
# runs, and the defaults leave too few for the root daemon's own watch.
cat > /etc/sysctl.d/99-vagrant-rtest.conf <<'EOF'
fs.inotify.max_user_instances = 8192
fs.inotify.max_user_watches = 1048576
EOF
sysctl --system >/dev/null
