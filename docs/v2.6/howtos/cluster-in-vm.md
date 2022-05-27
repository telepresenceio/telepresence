---
description: "Use Telepresence to intercept services in a cluster running in a hosted virtual machine."
---

# Network considerations for locally hosted clusters

## The problem
Telepresence creates a Virtual Network Interface ([VIF](../tun-device)) that maps the clusters subnets to the host machine when it connects. If you're running Kubernetes locally (e.g., k3s, Minikube, Docker for Desktop), you may encounter network problems because the devices in the host are also accessible from the cluster's nodes.

### Example:
A k3s cluster runs in a headless VirtualBox machine that uses a "host-only" network. This network will allow both host-to-guest and guest-to-host connections. In other words, the cluster will have access to the host's network and, while Telepresence is connected, also to its VIF. This means that from the cluster's perspective, there will now be more than one interface that maps the cluster's subnets; the ones already present in the cluster's nodes, and then the Telepresence VIF, mapping them again.

Now, if a request arrives to Telepresence that is covered by a subnet mapped by the VIF, the request is routed to the cluster. If the cluster for some reason doesn't find a corresponding listener that can handle the request, it will eventually try the host network, and find the VIF. The VIF routes the request to the cluster and now the recursion is in motion. The final outcome of the request will likely be a timeout but since the recursion is very resource intensive (a large amount of very rapid connection requests), this will likely also affect other connections in a bad way. 

## Solution

### Create a bridge network
A bridge network is a Link Layer (L2) device that forwards traffic between network segments. By creating a bridge network, you can bypass the host's network stack which enable the Kubernetes cluster to connect directly to the same router as your host.

To create a bridge network, you need to change the network settings of the guest running a cluster's node so that it connects directly to a physical network device on your host. The details on how to configure the bridge depends on what type of virtualization solution you're using.

### Vagrant + Virtualbox + k3s example
Here's a sample `Vagrantfile` that will spin up a server node and two agent nodes in three headless instances using a bridged network. It also adds the configuration needed for the cluster to host a docker repository (very handy in case you want to save bandwidth). The Kubernetes registry manifest must be applied using `kubectl -f registry.yaml` once the cluster is up and running.

#### Vagrantfile
```ruby
# -*- mode: ruby -*-
# vi: set ft=ruby :

# bridge is the name of the host's default network device
$bridge = 'wlp5s0'

# default_route should be the IP of the host's default route.
$default_route = '192.168.1.1'

# nameserver must be the IP of an external DNS, such as 8.8.8.8
$nameserver = '8.8.8.8'

# server_name should also be added to the host's /etc/hosts file and point to the server_ip
# for easy access when pushing docker images
server_name = 'multi'

# static IPs for the server and agents. Those IPs must be on the default router's subnet
server_ip = '192.168.1.110'
agents = {
  'agent1' => '192.168.1.111',
  'agent2' => '192.168.1.112',
}

# Extra parameters in INSTALL_K3S_EXEC variable because of
# K3s picking up the wrong interface when starting server and agent
# https://github.com/alexellis/k3sup/issues/306
server_script = <<-SHELL
    sudo -i
    apk add curl
    export INSTALL_K3S_EXEC="--bind-address=#{server_ip} --node-external-ip=#{server_ip} --flannel-iface=eth1"
    mkdir -p /etc/rancher/k3s
    cat <<-'EOF' > /etc/rancher/k3s/registries.yaml
mirrors:
  "multi:5000":
    endpoint:
      - "http://#{server_ip}:5000"
EOF
    curl -sfL https://get.k3s.io | sh -
    echo "Sleeping for 5 seconds to wait for k3s to start"
    sleep 5
    cp /var/lib/rancher/k3s/server/token /vagrant_shared
    cp /etc/rancher/k3s/k3s.yaml /vagrant_shared
    cp /etc/rancher/k3s/registries.yaml /vagrant_shared
    SHELL

agent_script = <<-SHELL
    sudo -i
    apk add curl
    export K3S_TOKEN_FILE=/vagrant_shared/token
    export K3S_URL=https://#{server_ip}:6443
    export INSTALL_K3S_EXEC="--flannel-iface=eth1"
    mkdir -p /etc/rancher/k3s
    cat <<-'EOF' > /etc/rancher/k3s/registries.yaml
mirrors:
  "multi:5000":
    endpoint:
      - "http://#{server_ip}:5000"
EOF
    curl -sfL https://get.k3s.io | sh -
    SHELL

def config_vm(name, ip, script, vm)
  # The network_script has two objectives:
  # 1. Ensure that the guest's default route is the bridged network (bypass the network of the host)
  # 2. Ensure that the DNS points to an external DNS service, as opposed to the DNS of the host that
  #    the NAT network provides.
  network_script = <<-SHELL
    sudo -i
    ip route delete default 2>&1 >/dev/null || true; ip route add default via #{$default_route}
    cp /etc/resolv.conf /etc/resolv.conf.orig
    sed 's/^nameserver.*/nameserver #{$nameserver}/' /etc/resolv.conf.orig > /etc/resolv.conf
  SHELL

  vm.hostname = name
  vm.network 'public_network', bridge: $bridge, ip: ip
  vm.synced_folder './shared', '/vagrant_shared'
  vm.provider 'virtualbox' do |vb|
    vb.memory = '4096'
    vb.cpus = '2'
  end
  vm.provision 'shell', inline: script
  vm.provision 'shell', inline: network_script, run: 'always'
end

Vagrant.configure('2') do |config|
  config.vm.box = 'generic/alpine314'

  config.vm.define 'server', primary: true do |server|
    config_vm(server_name, server_ip, server_script, server.vm)
  end

  agents.each do |agent_name, agent_ip|
    config.vm.define agent_name do |agent|
      config_vm(agent_name, agent_ip, agent_script, agent.vm)
    end
  end
end
```

The Kubernetes manifest to add the registry:

#### registry.yaml
```yaml
apiVersion: v1
kind: ReplicationController
metadata:
  name: kube-registry-v0
  namespace: kube-system
  labels:
    k8s-app: kube-registry
    version: v0
spec:
  replicas: 1
  selector:
    app: kube-registry
    version: v0
  template:
    metadata:
      labels:
        app: kube-registry
        version: v0
    spec:
      containers:
      - name: registry
        image: registry:2
        resources:
          limits:
            cpu: 100m
            memory: 200Mi
        env:
        - name: REGISTRY_HTTP_ADDR
          value: :5000
        - name: REGISTRY_STORAGE_FILESYSTEM_ROOTDIRECTORY
          value: /var/lib/registry
        volumeMounts:
        - name: image-store
          mountPath: /var/lib/registry
        ports:
        - containerPort: 5000
          name: registry
          protocol: TCP
      volumes:
      - name: image-store
        hostPath:
          path: /var/lib/registry-storage
---
apiVersion: v1
kind: Service
metadata:
  name: kube-registry
  namespace: kube-system
  labels:
    app: kube-registry
    kubernetes.io/name: "KubeRegistry"
spec:
  selector:
    app: kube-registry
  ports:
  - name: registry
    port: 5000
    targetPort: 5000
    protocol: TCP
  type: LoadBalancer
```

