---
title: Troubleshooting
description: "Learn how to troubleshoot common issues related to Telepresence, including intercept issues, cluster connection issues, and errors related to Ambassador Cloud."
---

# Troubleshooting

## Connecting to a cluster via VPN doesn't work.

There are a few different issues that could arise when working with a VPN. Please see the [dedicated page](reference/vpn.md) on Telepresence and VPNs to learn more on how to fix these.

## Connecting to a cluster hosted in a Docker Container or a VM on the workstation doesn't work

The cluster probably has access to the host's network and gets confused when it is mapped by Telepresence.
Please check the [cluster in hosted container or vm](howtos/cluster-in-vm.md) for more details.

## Volume mounts are not working on macOS

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

## Volume mounts are not working on Linux
It's necessary to have `sshfs` installed in order for volume mounts to work correctly when Telepresence engages with remote containers.

After you've installed `sshfs`, if mounts still aren't working:
1. Uncomment `user_allow_other` in `/etc/fuse.conf`
2. Add your user to the "fuse" group with: `sudo usermod -a -G fuse <your username>`
3. Restart your computer after uncommenting `user_allow_other` 

## DNS is broken on macOS

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

## Helm install failes with "uncomparable type" error

An attempt to install the traffic-manager using the `helm` command ends with an error similar to: 
```
Error: INSTALLATION FAILED: template: telepresence-oss/templates/deployment.yaml:172:22: executing "telepresence-oss/templates/deployment.yaml" at <eq .initSecurityContext nil>: error calling eq: uncomparable type map[string]interface {}: map[capabilities:map[add:[NET_ADMIN]]]
```
This will happen when you are using `helm` directly (as opposed to `telepresence helm`) and your helm version is older
than 3.11.3. To resolve this, you can upgrade your helm to a more recent version.

## No Sidecar Injected in GKE private clusters

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

## Injected init-container doesn't function properly

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
`tagertPort` that references a `containerPort` in the workload. When this isn't the case, it will instead do the
following during the injection of the traffic-agent:

1. Rename the designated container's port by prefixing it (i.e., containerPort: http becomes containerPort: tm-http).
2. Let the container port of our injected traffic-agent use the original name (i.e., containerPort: http).

Kubernetes takes care of the rest and will now associate the service's `targetPort` with our traffic-agent's
`containerPort`.

> [!IMPORTANT]
> If the service is "headless" (using `ClusterIP: None`), then using named ports won't help because the `targetPort` will
> not get remapped. A headless service will always require the init-container.

## EKS, Calico, and Traffic Agent injection timeouts

When using EKS with Calico CNI, the Kubernetes API server cannot reach the mutating webhook
used for triggering the traffic agent injection. To solve this problem, try providing the
Helm chart value `"hostNetwork=true"` when installing or upgrading the traffic-manager.

More information can be found in this [blog post](https://medium.com/@denisstortisilva/kubernetes-eks-calico-and-custom-admission-webhooks-a2956b49bd0d).

## Error connecting to GKE or EKS cluster

GKE and EKS require a plugin that utilizes their resepective IAM providers. 
You will need to install the [gke](install/cloud#gke-authentication-plugin) or [eks](install/cloud#eks-authentication-plugin) plugins 
for Telepresence to connect to your cluster.

## `too many files open` error when running `telepresence connect` on Linux

If `telepresence connect` on linux fails with a message in the logs `too many files open`, then check if `fs.inotify.max_user_instances` is set too low. Check the current settings with `sysctl fs.inotify.max_user_instances` and increase it temporarily with `sudo sysctl -w fs.inotify.max_user_instances=512`. For more information about permanently increasing it see [Kernel inotify watch limit reached](https://unix.stackexchange.com/a/13757/514457).
