# Networking through Virtual Network Interface

The Telepresence daemon process creates a Virtual Network Interface (VIF) when Telepresence connects to the cluster. The VIF ensures that the cluster's subnets are available to the workstation. It also intercepts DNS requests and forwards them to the traffic-manager which in turn forwards them to intercepted agents, if any, or performs a host lookup by itself.

### TUN-Device
The VIF is a TUN-device, which means that it communicates with the workstation in terms of L3 IP-packets. The router will recognize UDP and TCP packets and tunnel their payload to the traffic-manager via its encrypted gRPC API. The traffic-manager will then establish corresponding connections in the cluster. All protocol negotiation takes place in the client because the VIF takes care of the L3 to L4 translation (i.e. the tunnel is L4, not L3).

## Gains when using the VIF

### Both TCP and UDP
The TUN-device is capable of routing both TCP and UDP for outbound traffic. Earlier versions of Telepresence would only allow TCP. Future enhancements might be to also route inbound UDP, and perhaps a selection of ICMP packages (to allow for things like `ping`).

### No SSH required

The VIF approach is somewhat similar to using `sshuttle` but without
any requirements for extra software, configuration or connections.
Using the VIF means that only one single connection needs to be
forwarded through the Kubernetes apiserver (à la `kubectl
port-forward`), using only one single port.  There is no need for
`ssh` in the client nor for `sshd` in the traffic-manager.  This also
means that the traffic-manager container can run as the default user.

#### sshfs without ssh encryption
When a POD is intercepted, and its volumes are mounted on the local machine, this mount is performed by [sshfs](https://github.com/libfuse/sshfs). Telepresence will run `sshfs -o slave` which means that instead of using `ssh` to establish an encrypted communication to an `sshd`, which in turn terminates the encryption and forwards to `sftp`, the `sshfs` will talk `sftp` directly on its `stdin/stdout` pair. Telepresence tunnels that directly to an `sftp` in the agent using its already encrypted gRPC API. As a result, no `sshd` is needed in client nor in the traffic-agent, and the traffic-agent container can run as the default user.

### No Firewall rules
With the VIF in place, there's no longer any need to tamper with firewalls in order to establish IP routes. The VIF makes the cluster subnets available during connect, and the kernel will perform the routing automatically. When the session ends, the kernel is also responsible for cleaning up.
