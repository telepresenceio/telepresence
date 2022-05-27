# Volume mounts

import Alert from '@material-ui/lab/Alert';

Telepresence supports locally mounting of volumes that are mounted to your Pods.  You can specify a command to run when starting the intercept, this could be a subshell or local server such as Python or Node.

```
telepresence intercept <mysvc> --port <port> --mount=/tmp/ -- /bin/bash
```

In this case, Telepresence creates the intercept, mounts the Pod's volumes to locally to `/tmp`, and starts a Bash subshell.

Telepresence can set a random mount point for you by using `--mount=true` instead, you can then find the mount point in the output of `telepresence list` or using the `$TELEPRESENCE_ROOT` variable.

```
$ telepresence intercept <mysvc> --port <port> --mount=true -- /bin/bash
Using Deployment <mysvc>
intercepted
    Intercept name    : <mysvc>
    State             : ACTIVE
    Workload kind     : Deployment
    Destination       : 127.0.0.1:<port>
    Volume Mount Point: /var/folders/cp/2r22shfd50d9ymgrw14fd23r0000gp/T/telfs-988349784
    Intercepting      : all TCP connections

bash-3.2$ echo $TELEPRESENCE_ROOT
/var/folders/cp/2r22shfd50d9ymgrw14fd23r0000gp/T/telfs-988349784
```

<Alert severity="info"><code>--mount=true</code> is the default if a <code>mount</code> option is not specified, use <code>--mount=false</code> to disable mounting volumes.</Alert>

With either method, the code you run locally either from the subshell or from the intercept command will need to be prepended with the `$TELEPRESENCE_ROOT` environment variable to utilize the mounted volumes.

For example, Kubernetes mounts secrets to `/var/run/secrets/kubernetes.io` (even if no `mountPoint` for it exists in the Pod spec).  Once mounted, to access these you would need to change your code to use `$TELEPRESENCE_ROOT/var/run/secrets/kubernetes.io`.

<Alert severity="info">If using <code>--mount=true</code> without a command, you can use either <a href="../environment/">environment variable</a> flag to retrieve the variable.</Alert>
