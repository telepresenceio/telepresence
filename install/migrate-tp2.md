# Migrate to Telepresence 2 from Legacy Telepresence

Telepresence 2 (the current major version) has different mechanics and requires a different mental model from [Telepresence 1](https://www.telepresence.io/) when working with local instances of your services.

In Telepresence 1, a pod running a service is swapped with a pod running the Telepresence proxy. This proxy receives traffic intended for the service, and sends the traffic onward to the target workstation or laptop. We called this mechanism "swap-deployment".

In practice, this mechanism, while simple in concept, had some challenges. Losing the connection to the cluster would leave the deployment in an inconsistent state. Swapping the pods would take time.

Telepresence 2 introduces a [new architecture](../../reference/architecture/) built around "intercepts" that addresses this problem. With Telepresence 2, a sidecar proxy is injected onto the pod. The proxy then intercepts traffic intended for the pod and routes it to the workstation/laptop. The advantage of this approach is that the service is running at all times, and no swapping is used. By using the proxy approach, we can also do selective intercepts, where certain types of traffic get routed to the service while other traffic gets routed to your laptop/workstation.

Please see [the Telepresence quick start](../../quick-start/) for an introduction to running intercepts and [the intercept reference doc](../../reference/intercepts/) for a deep dive into intercepts

## Using Telepresence 1 Commands 

First please ensure you've [installed Telepresence 2](../).

Telepresence 2 is able to translate common Telepresence 1 commands into native Telepresence 2 commands.  
So if you want to get started quickly, you can just use the same old telepresence commands you are used
to with the telepresence 2 binary.  

For example, say you have a deployment (myserver) that you want to swap deployment (equivalent to intercept in
telepresence 2) with a python server, you could run the following command:

```
$ telepresence --swap-deployment myserver --expose 9090 --run python3 -m http.server 9090
< help text >

Legacy telepresence command used
Command roughly translates to the following in Telepresence 2:
telepresence intercept echo-easy --port 9090 -- python3 -m http.server 9090
running...
Connecting to traffic manager...
Connected to context <your k8s cluster>
Using Deployment myserver
intercepted
    Intercept name    : myserver
    State             : ACTIVE
    Workload kind     : Deployment
    Destination       : 127.0.0.1:9090
    Intercepting      : all TCP connections
Serving HTTP on :: port 9090 (http://[::]:9090/) ...
```
Telepresence will let you know what the telepresence 1 command has mapped to and automatically
runs it.  So you can get started with Telepresence 2 today, using the commands you are used to
and it will help you learn the Telepresence 2 syntax.

### Telepresence 1 Commands Limitations
Some of the commands and flags from Telepresence 1 either didn't apply to telepresence 2 or
aren't yet supported in telepresence 2.  For some known popular commands, such as --method,
telepresence will include output letting you know that the flag has went away. For flags that
Telepresence 2 can't translate yet, it will let you know that that flag is "unknown".

If we are missing any flags or functionality that is integral to your usage, please let us know
by [creating an issue](https://github.com/telepresenceio/telepresence/issues) and/or talking to us on our [Slack channel](https://a8r.io/Slack)!

## Telepresence Changes
Since the new architecture deploys a traffic-manager into the Ambassador namespace, please take a look at
our [rbac guide](../../reference/rbac) if you run into any issues with permissions while updating to telepresence 2.

Telepresence 2 installs the traffic-manager in the cluster and traffic-agents when performing intercepts (including
with --swap-deployment) and leaves them.  If you use --swap-deployment, the intercept will be left once the process
dies, but the agent will remain. There's no harm in leaving the agent running alongside your service, but when you
want to remove them from the cluster, the telepresence uninstall command will help:
```
$ telepresence uninstall --help
Uninstall telepresence agents and manager

Usage:
  telepresence uninstall [flags] { --agent <agents...> |--all-agents | --everything }

Flags:
  -d, --agent              uninstall intercept agent on specific deployments
  -a, --all-agents         uninstall intercept agent on all deployments
  -e, --everything         uninstall agents and the traffic manager
  -h, --help               help for uninstall
  -n, --namespace string   If present, the namespace scope for this CLI request
```
