---
title: Volume mounts
hide_table_of_contents: true
---
# Volume mounts

Volume mounts are achieved using a Docker Volume plug-in and Docker volume mounts when connecting using `--docker` and using `--docker-run`. This page
describes how mounts are achieved when running directly on the host.

Telepresence supports locally mounting of volumes that are mounted to your Pods.  You can specify a command to run when starting the attachment, this could be a subshell or local server such as Python or Node.

```
telepresence replace <workload> --mount=/tmp/ -- /bin/bash
```

In this case, Telepresence replaces the remote container, mounts the Pod's volumes to locally to `/tmp`, and starts a Bash subshell.

Telepresence can set a random mount point for you by using `--mount=true` instead, you can then find the mount point in the output of `telepresence list` or using the `$TELEPRESENCE_ROOT` variable.

```
$ telepresence replace <workload> --mount=true -- /bin/bash
Using Deployment <mysvc>
replaced
    Container name    : <mycontainer>
    State             : ACTIVE
    Workload kind     : Deployment
    Destination       : 127.0.0.1:<port>
    Volume Mount Point: /var/folders/cp/2r22shfd50d9ymgrw14fd23r0000gp/T/telfs-988349784

bash-3.2$ echo $TELEPRESENCE_ROOT
/var/folders/cp/2r22shfd50d9ymgrw14fd23r0000gp/T/telfs-988349784
```

> [!NOTE]
> `--mount=true` is the default if a mount option is not specified, use `--mount=false` to disable mounting volumes.

With either method, the code you run locally either from the subshell or from the `replace` command will need to be prepended with the `$TELEPRESENCE_ROOT` environment variable to utilize the mounted volumes.

For example, Kubernetes mounts secrets to `/var/run/secrets/kubernetes.io` (even if no `mountPoint` for it exists in the Pod spec).  Once mounted, to access these you would need to change your code to use `$TELEPRESENCE_ROOT/var/run/secrets/kubernetes.io`.

> [!NOTE]
> If using `--mount=true` without a command, you can use either [environment variable](environment.md) flag to retrieve the variable.

## How the mounts work

When the volumes of an attached POD are mounted on the local machine, the mount is performed by [sshfs](https://github.com/libfuse/sshfs). Telepresence runs `sshfs -o slave`, which means that instead of using `ssh` to establish an encrypted communication to an `sshd`, which in turn terminates the encryption and forwards to `sftp`, the `sshfs` will talk `sftp` directly on its `stdin/stdout` pair. Telepresence tunnels that directly to an `sftp` in the agent using its already encrypted gRPC API. As a result, no `sshd` is needed in the client nor in the traffic-agent, and the traffic-agent container can run as the default user.
