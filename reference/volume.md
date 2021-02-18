# Volume Mounts

import Alert from '@material-ui/lab/Alert';

Telepresence supports locally mounting of volumes that are mounted to your Pods.  You can specify a command to run when starting the intercept, this could be a subshell or local server such as Python or Node.

```
telepresence intercept <mysvc> --port <port> --mount=/tmp/ -- /bin/bash
```

In this case, Telepresence creates the intercept, mounts the Pod's volumes to locally to `/tmp`, and starts a Bash subshell.

Telepresence can set a random mount point for you by using `--mount=true` instead, you can then find the mount point using the `$TELEPRESENCE_ROOT` variable.

```
$ telepresence intercept <mysvc> --port <port> --mount=true -- /bin/bash
Using deployment <mysvc>
intercepted
    State       : ACTIVE
    Destination : 127.0.0.1:<port>
    Intercepting: all connections

bash-3.2$ echo $TELEPRESENCE_ROOT
/var/folders/yh/42y5h_7s5992f80sjlv3wlgc0000gn/T/telfs-427288831
```

<Alert severity="info"><code>--mount=true</code> is the default if a <code>mount</code> option is not specified, use <code>--mount=false</code> to disable mounting volumes.</Alert>

With either method, the code you run locally either from the subshell or from the intercept command will need to be prepended with the `$TELEPRESENCE_ROOT` environment variable to utilitze the mounted volumes.

For example, Kubernetes mounts secrets to `/var/run/secrets`.  Once mounted, to access these you would need to change your code to use `$TELEPRESENCE_ROOT/var/run/secrets`.

<Alert severity="info">If using <code>--mount=true</code> without a command, you can use either <a href="../environment/">environment variable</a> flag to retrieve the variable.</Alert>