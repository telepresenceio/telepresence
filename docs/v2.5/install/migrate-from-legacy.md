# Migrate from legacy Telepresence

Telepresence (formerly referenced as Telepresence 2, which is the current major version) has different mechanics and requires a different mental model from [legacy Telepresence 1](https://www.telepresence.io/docs/v1/) when working with local instances of your services.

In legacy Telepresence, a pod running a service was swapped with a pod running the Telepresence proxy. This proxy received traffic intended for the service, and sent the traffic onward to the target workstation or laptop. We called this mechanism "swap-deployment".

In practice, this mechanism, while simple in concept, had some challenges. Losing the connection to the cluster would leave the deployment in an inconsistent state. Swapping the pods would take time.

Telepresence 2 introduces a [new
architecture](../../reference/architecture/) built around "intercepts"
that addresses these problems.  With the new Telepresence, a sidecar
proxy ("traffic agent") is injected onto the pod.  The proxy then
intercepts traffic intended for the Pod and routes it to the
workstation/laptop.  The advantage of this approach is that the
service is running at all times, and no swapping is used.  By using
the proxy approach, we can also do personal intercepts, where rather
than re-routing all traffic to the laptop/workstation, it only
re-routes the traffic designated as belonging to that user, so that
multiple developers can intercept the same service at the same time
without disrupting normal operation or disrupting eacho.

Please see [the Telepresence quick start](../../quick-start/) for an introduction to running intercepts and [the intercept reference doc](../../reference/intercepts/) for a deep dive into intercepts.

## Using legacy Telepresence commands

First please ensure you've [installed Telepresence](../).

Telepresence is able to translate common legacy Telepresence commands into native Telepresence commands.
So if you want to get started quickly, you can just use the same legacy Telepresence commands you are used
to with the Telepresence binary.

For example, say you have a deployment (`myserver`) that you want to swap deployment (equivalent to intercept in
Telepresence) with a python server, you could run the following command:

```
$ telepresence --swap-deployment myserver --expose 9090 --run python3 -m http.server 9090
< help text >

Legacy telepresence command used
Command roughly translates to the following in Telepresence:
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

Telepresence will let you know what the legacy Telepresence command has mapped to and automatically
runs it.  So you can get started with Telepresence today, using the commands you are used to
and it will help you learn the Telepresence syntax.

### Legacy command mapping

Below is the mapping of legacy Telepresence to Telepresence commands (where they exist and
are supported).

| Legacy Telepresence Command                      | Telepresence Command                       |
|--------------------------------------------------|--------------------------------------------|
| --swap-deployment $workload                      | intercept $workload                        |
| --expose localPort[:remotePort]                  | intercept --port localPort[:remotePort]    |
| --swap-deployment $workload --run-shell          | intercept $workload -- bash                |
| --swap-deployment $workload --run $cmd           | intercept $workload -- $cmd                |
| --swap-deployment $workload --docker-run $cmd    | intercept $workload --docker-run -- $cmd   |
| --run-shell                                      | connect -- bash                            |
| --run $cmd                                       | connect -- $cmd                            |
| --env-file,--env-json                            | --env-file, --env-json (haven't changed)   |
| --context,--namespace                            | --context, --namespace (haven't changed)   |
| --mount,--docker-mount                           | --mount, --docker-mount (haven't changed)  |

### Legacy Telepresence command limitations

Some of the commands and flags from legacy Telepresence either didn't apply to Telepresence or
aren't yet supported in Telepresence.  For some known popular commands, such as --method,
Telepresence will include output letting you know that the flag has gone away. For flags that
Telepresence can't translate yet, it will let you know that that flag is "unsupported".

If Telepresence is missing any flags or functionality that is integral to your usage, please let us know
by [creating an issue](https://github.com/telepresenceio/telepresence/issues) and/or talking to us on our [Slack channel](https://a8r.io/Slack)!

## Telepresence changes

Telepresence installs a Traffic Manager in the cluster and Traffic Agents alongside workloads when performing intercepts (including
with `--swap-deployment`) and leaves them.  If you use `--swap-deployment`, the intercept will be left once the process
dies, but the agent will remain. There's no harm in leaving the agent running alongside your service, but when you
want to remove them from the cluster, the following Telepresence command will help:
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

Since the new architecture deploys a Traffic Manager into the Ambassador namespace, please take a look at
our [rbac guide](../../reference/rbac) if you run into any issues with permissions while upgrading to Telepresence.
