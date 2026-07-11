---
title: Troubleshooting
description: "How to troubleshoot common Telepresence issues: cluster connections, DNS, volume mounts, and traffic-agent injection."
---

# Troubleshooting

## Connection issues

### Connecting to a cluster via VPN doesn't work.

There are a few different issues that could arise when working with a VPN. Please see the [dedicated page](reference/vpn.md) on Telepresence and VPNs to learn more on how to fix these.

### Connecting to a cluster hosted in a Docker Container or a VM on the workstation doesn't work

The cluster probably has access to the host's network and gets confused when it is mapped by Telepresence.
Please check the [cluster in hosted container or vm](howtos/cluster-in-vm.md) for more details.

### Error connecting to GKE or EKS cluster

GKE and EKS require a plugin that utilizes their respective IAM providers. 
You will need to install the [gke](install/cloud#gke-authentication-plugin) or [eks](install/cloud#eks-authentication-plugin) plugins 
for Telepresence to connect to your cluster.

### `too many files open` error when running `telepresence connect` on Linux

If `telepresence connect` on linux fails with a message in the logs `too many files open`, then check if `fs.inotify.max_user_instances` is set too low. Check the current settings with `sysctl fs.inotify.max_user_instances` and increase it temporarily with `sudo sysctl -w fs.inotify.max_user_instances=512`. For more information about permanently increasing it see [Kernel inotify watch limit reached](https://unix.stackexchange.com/a/13757/514457).

## DNS issues

### DNS is broken on macOS

Commands like `dig` cannot find cluster resources even though Telepresence is connected to the cluster, but it works
with `curl`.

This is because `dig`, and some other utilities on macOS have their own built-in DNS client which bypasses the macOS
native DNS system and use the libc resolver directly. Here's an excerpt from the `dig` command's man-page:
> Mac OS X NOTICE
> 
> The nslookup command does not use the host name and address resolution or the DNS query routing
> mechanisms used by other processes running on Mac OS X.  The results of name or address queries
> printed by nslookup may differ from those found by other processes that use the Mac OS X native
> name and address resolution mechanisms.  The results of DNS queries may also differ from queries
> that use the Mac OS X DNS routing library.

A command that should always work is:
```console
$ dscacheutil -q host -a name <name to resolve>
```

### DNS does not resolve in GitLab pipeline

If services are not resolving after running `telepresence connect` in a GitLab pipeline, this may be because the `resolv.conf` file is bind-mounted, which prevents it from being copied, deleted, or moved. However, you can still replace its contents.
```yaml
job:
  ...
  script:
    - telepresence connect
    - echo "nameserver 127.0.0.1" > /etc/resolv.conf # Telepresence runs a DNS server on port 53 but cannot update the bind-mounted resolv.conf file
```

## Volume mount issues

### Volume mounts are not working on macOS

It's necessary to have `sshfs` installed in order for volume mounts to work correctly during intercepts. Lately there's been some issues using `brew install sshfs` a macOS workstation because the required component `osxfuse` (now named `macfuse`) isn't open source and hence, no longer supported. As a workaround, you can now use `gromgit/fuse/sshfs-mac` instead. Follow these steps:

1. Remove old sshfs, macfuse, osxfuse using `brew uninstall`
2. `brew install --cask macfuse`
3. `brew install gromgit/fuse/sshfs-mac`
4. `brew link --overwrite sshfs-mac`

Now sshfs -V shows you the correct version, e.g.:
```
$ sshfs -V
SSHFS version 2.10
FUSE library version: 2.9.9
fuse: no mount point
```

5. Next, try a mount (or an replace/ingest/intercept that performs a mount). It will fail because you need to give permission to “Benjamin Fleischer” to execute a kernel extension (a pop-up appears that takes you to the system preferences).
6. Approve the needed permission
7. Reboot your computer.

### Volume mounts are not working on Linux
It's necessary to have `sshfs` installed in order for volume mounts to work correctly when Telepresence attaches to remote containers.

After you've installed `sshfs`, if mounts still aren't working:
1. Uncomment `user_allow_other` in `/etc/fuse.conf`
2. Add your user to the "fuse" group with: `sudo usermod -a -G fuse <your username>`
3. Restart your computer after uncommenting `user_allow_other` 

## Traffic-agent injection issues

### No Sidecar Injected in GKE private clusters

An attempt to `telepresence intercept` results in a timeout, and upon examination of the pods (`kubectl get pods`) it's discovered that the intercept command did not inject a sidecar into the workload's pods:

```bash
$ kubectl get pod
NAME                         READY   STATUS    RESTARTS   AGE
echo-easy-7f6d54cff8-rz44k   1/1     Running   0          5m5s

$ telepresence intercept echo-easy -p 8080
telepresence: error: connector.CreateIntercept: request timed out while waiting for agent echo-easy.default to arrive
$ kubectl get pod
NAME                        READY   STATUS    RESTARTS   AGE
echo-easy-d8dc4cc7c-27567   1/1     Running   0          2m9s

# Notice how 1/1 containers are ready.
```

If this is occurring in a GKE cluster with private networking enabled, it is likely due to firewall rules blocking the
Traffic Manager's webhook injector from the API server.
To fix this, add a firewall rule allowing your cluster's master nodes to access TCP port `8443` in your cluster's pods,
or change the port number that Telepresence is using for the agent injector by providing the number of an allowed port
using the Helm chart value `agentInjector.webhook.port`.
Please refer to the [telepresence install instructions](install/cloud#gke) or the [GCP docs](https://cloud.google.com/kubernetes-engine/docs/how-to/private-clusters#add_firewall_rules) for information to resolve this.

Attachments that use the [node-agent](reference/node-agent.md) do not involve the webhook at all and are unaffected
by API-server-to-webhook connectivity problems like this one.

### Injected init-container doesn't function properly

The init-container is injected to insert `iptables` rules that redirects port numbers from the app container to the
traffic-agent sidecar. This is necessary when the service's `targetPort` is numeric. It requires elevated privileges
(`NET_ADMIN` capabilities), and the inserted rules may get overridden by `iptables` rules inserted by other vendors,
such as Istio or Linkerd.

Injection of the init-container can often be avoided by using a `targetPort` _name_ instead of a number, and  ensure
that  the corresponding container's `containerPort` is also named. This example uses the name "http", but any valid
name will do:
```yaml
apiVersion: v1
kind: Pod
metadata:
  ...
spec:
  containers:
    - ports:
      - name: http
        containerPort: 8080
---
apiVersion: v1
kind: Service
metadata:
  ...
spec:
  ports:
    - port: 80
      targetPort: http
```

Telepresence injects an init-container into the pods of a workload, only if at least one service specifies a numeric
`targetPort` that references a `containerPort` in the workload. When this isn't the case, it will instead do the
following during the injection of the traffic-agent:

1. Rename the designated container's port by prefixing it (i.e., containerPort: http becomes containerPort: tm-http).
2. Let the container port of our injected traffic-agent use the original name (i.e., containerPort: http).

Kubernetes takes care of the rest and will now associate the service's `targetPort` with our traffic-agent's
`containerPort`.

> [!IMPORTANT]
> If the service is "headless" (using `ClusterIP: None`), then using named ports won't help because the `targetPort` will
> not get remapped. A headless service will always require the init-container.

### EKS, Calico, and Traffic Agent injection timeouts

When using EKS with the Calico CNI, the Kubernetes API server often cannot reach the
agent-injector mutating webhook that triggers traffic-agent injection. The API server runs in
the AWS-managed control plane, outside the Calico-managed pod network, so it has no route to the
webhook's `ClusterIP` Service. Because the webhook uses `failurePolicy: Ignore`, the API server
admits pods *unmodified* when it can't reach it: your workload rolls out normally, but no
traffic-agent sidecar is injected. An intercept then fails with a message like
"request timed out while waiting for agent ... to arrive", and the pods you see have no
`traffic-agent` container.

The blog post [Kubernetes, EKS, Calico, and custom admission webhooks](https://medium.com/@denisstortisilva/kubernetes-eks-calico-and-custom-admission-webhooks-a2956b49bd0d)
describes the underlying problem in detail.

#### Decision path

Work through these options in order; the first that is acceptable for your environment is the
one to use.

1. **Try `hostNetwork=true` first.** Installing or upgrading the traffic-manager with the Helm
   value `hostNetwork=true` places the agent-injector on the node's host network, which the API
   server can reach the same way it reaches the kubelet. This is the simplest fix and requires no
   external exposure.

   ```console
   $ telepresence helm upgrade --set hostNetwork=true
   ```

   If your security posture forbids host networking for the traffic-manager, move to the next
   option.

2. **Expose the webhook with a NodePort or LoadBalancer and point the webhook at it by URL.**
   Instead of having the API server resolve the in-cluster Service, you give the
   `MutatingWebhookConfiguration` an explicit `url`, backed by a Service the control plane *can*
   reach.

   - **NodePort** — exposes the webhook on every node's IP at a fixed port:

     ```yaml
     agentInjector:
       service:
         type: NodePort
         nodePort: 30443
       webhook:
         # Reachable from the control plane; e.g. a node IP or an internal DNS name.
         url: "https://<node-host-or-ip>:30443/traffic-agent"
       certificate:
         altNames:
           - "<node-host-or-ip>"
     ```

   - **LoadBalancer** — exposes the webhook behind a (preferably internal) AWS load balancer.
     Use `agentInjector.service.port` to front the standard `443` while the traffic-manager
     container keeps binding the unprivileged `webhook.port` (`8443`):

     ```yaml
     agentInjector:
       service:
         type: LoadBalancer
         port: 443
       webhook:
         url: "https://<lb-hostname>:443/traffic-agent"
       certificate:
         altNames:
           - "<lb-hostname>"
     ```

   The `webhook.url` path must match `agentInjector.webhook.servicePath` (default
   `/traffic-agent`). The port in the URL must match the exposed port: the NodePort you pinned,
   or `agentInjector.service.port` for a LoadBalancer. Do **not** set `agentInjector.webhook.port`
   to a privileged port such as `443` — that is the port the non-root traffic-manager container
   binds, and it cannot bind ports below 1024. Use `agentInjector.service.port` instead, which
   only affects the Service and still targets the container's `8443` port.

#### Restrict access to the exposed webhook

Exposing the webhook outside the cluster widens its attack surface, so lock it down with the
network so that **only the EKS control plane** can reach it:

- For a **NodePort**, add an inbound rule to the worker nodes' security group that allows the
  chosen node port only from the **EKS cluster security group** (the security group associated
  with the control plane / the cluster's ENIs). Deny it from everywhere else.
- For an **internal LoadBalancer**, scope its security group / source ranges the same way and
  keep the load balancer internal (`service.beta.kubernetes.io/aws-load-balancer-internal`) so
  it is never reachable from the public internet.

#### TLS hostname and SAN requirements

The API server validates the webhook's serving certificate against the host it connects to.
Whatever host you put in `agentInjector.webhook.url` (node IP/DNS name or load-balancer
hostname) **must** appear as a Subject Alternative Name on the certificate, or the TLS handshake
fails and injection silently stops again. Add every such host to
`agentInjector.certificate.altNames` (shown above); this works for both the Helm-generated
certificate and the `cert-manager` path. Entries are classified automatically: bare IPv4/IPv6
addresses become IP SANs and everything else becomes a DNS SAN, so both
`https://<node-ip>:30443/...` and `https://<hostname>:443/...` URLs validate correctly. The
default in-cluster Service DNS names (`agent-injector.<namespace>` and
`agent-injector.<namespace>.svc`) are always included automatically.

> [!IMPORTANT]
> Exposing the webhook externally is usually an *upgrade* of an existing traffic-manager. When
> the certificate was generated by Helm (`agentInjector.certificate.method=helm`, the default),
> adding `altNames` on an upgrade does **not** rotate the already-stored serving certificate, so
> the new SANs never take effect. Set `agentInjector.certificate.regenerate=true` on the upgrade
> that introduces the `altNames`:
>
> ```console
> $ telepresence helm upgrade --values external-webhook.yaml --set agentInjector.certificate.regenerate=true
> ```
>
> This does not apply to the `cert-manager` method, which reissues the certificate automatically
> when its `dnsNames`/`ipAddresses` change.

#### `failurePolicy` tradeoffs

The webhook ships with `failurePolicy: Ignore` (`agentInjector.webhook.failurePolicy`). The
tradeoff:

- **`Ignore` (default)** — if the API server can't reach the webhook, pods are admitted without
  a sidecar. Your application keeps running, but intercepts silently fail to inject (the symptom
  this page describes). Safer for cluster availability.
- **`Fail`** — pod creation is *rejected* when the webhook is unreachable. This turns a silent
  injection failure into a loud, immediate error, which makes the misconfiguration obvious, but
  it also means **any** workload creation in a managed namespace breaks while the webhook is
  unreachable. Only choose `Fail` if you accept that blast radius.

If you keep the default `Ignore`, the absence of a `traffic-agent` container in freshly created
pods (combined with an intercept timeout) is your signal that the API server isn't reaching the
webhook — return to the decision path above.

## Routing issues

### Routing loops when accessing deleted or non-existent service IPs on local clusters

On local Kubernetes clusters (Kind, minikube, k3d, Docker Desktop), accessing a deleted or
never-assigned service ClusterIP through the Telepresence TUN device can cause a routing loop.
The packet is forwarded to the traffic-agent, which re-dials the same IP; with no kube-proxy
rule in place the packet escapes the cluster via the node's default route, returns to the
workstation, and the cycle repeats until the connection times out.

Enable the **route-controller** DaemonSet to prevent this. It installs an nftables `forward`
chain drop rule for the service CIDR on every node, ensuring that packets bound for
non-existent ClusterIPs are silently dropped rather than escaping the cluster.

See [Routing Loop Prevention on Local Clusters](reference/route-controller.md) for
installation and configuration instructions.

## Installation issues

### Helm install fails with "uncomparable type" error

An attempt to install the traffic-manager using the `helm` command ends with an error similar to: 
```
Error: INSTALLATION FAILED: template: telepresence-oss/templates/deployment.yaml:172:22: executing "telepresence-oss/templates/deployment.yaml" at <eq .initSecurityContext nil>: error calling eq: uncomparable type map[string]interface {}: map[capabilities:map[add:[NET_ADMIN]]]
```
This will happen when you are using `helm` directly (as opposed to `telepresence helm`) and your helm version is older
than 3.11.3. To resolve this, you can upgrade your helm to a more recent version.
