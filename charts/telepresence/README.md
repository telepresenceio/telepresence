# Telepresence

[Telepresence](https://www.getambassador.io/products/telepresence/) is a tool
that allows for local development of microservices running in a remote
Kubernetes cluster.

This chart manages the server-side components of Telepresence so that an
operations team can give limited access to the cluster for developers to work on
their services.

## Install

The telepresence binary embeds the helm chart, so the easiest way to install is:

```sh
$ telepresence helm install [--set x=y | --values &lt;values file&gt;]
```

## Configuration

The following tables lists the configurable parameters of the Telepresence chart and their default values.

| Parameter                                            | Description                                                                                                                 | Default                                                                     |
|------------------------------------------------------|-----------------------------------------------------------------------------------------------------------------------------|-----------------------------------------------------------------------------|
| image.registry                                       | The repository to download the image from. Set `TELEPRESENCE_REGISTRY=image.registry` locally if changing this value.       | `docker.io/datawire`                                                        |
| image.name                                           | The name of the image to use for the traffic-manager                                                                        | `tel2`                                                                      |
| image.pullPolicy                                     | How the `Pod` will attempt to pull the image.                                                                               | `IfNotPresent`                                                              |
| image.tag                                            | Override the version of the Traffic Manager to be installed.                                                                | `""` (Defined in `appVersion` Chart.yaml)                                   |
| image.imagePullSecrets                               | The `Secret` storing any credentials needed to access the image in a private registry.                                      | `[]`                                                                        |
| apiPort                                              | The port used by the Traffic Manager gRPC API                                                                               | 8081                                                                        |
| podLabels                                            | Labels for the Traffic Manager `Pod`                                                                                        | `{}`                                                                        |
| podAnnotations                                       | Annotations for the Traffic Manager `Pod`                                                                                   | `{}`                                                                        |
| podCIDRs                                             | Verbatim list of CIDRs that the cluster uses for pods. Only valid together with `podCIDRStrategy: environment`              | `[]`                                                                        |
| podCIDRStrategy                                      | Define the strategy that the traffic-manager uses to discover what CIDRs the cluster uses for pods                          | `auto`                                                                      |
| podSecurityContext                                   | The Kubernetes SecurityContext for the `Pod`                                                                                | `{}`                                                                        |
| securityContext                                      | The Kubernetes SecurityContext for the `Deployment`                                                                         | `{"readOnlyRootFilesystem": true, "runAsNonRoot": true, "runAsUser": 1000}` |
| nodeSelector                                         | Define which `Node`s you want to the Traffic Manager to be deployed to.                                                     | `{}`                                                                        |
| tolerations                                          | Define tolerations for the Traffic Manager to ignore `Node` taints.                                                         | `[]`                                                                        |
| affinity                                             | Define the `Node` Affinity and Anti-Affinity for the Traffic Manager.                                                       | `{}`                                                                        |
| priorityClassName                                    | Name of the existing priority class to be used                                                                              | `""`                                                                        |
| service.type                                         | The type of `Service` for the Traffic Manager.                                                                              | `ClusterIP`                                                                 |
| livenessProbe                                        | Define livenessProbe for the Traffic Manger.                                                                                | `{}`                                                                        |
| readinessProbe                                       | Define readinessProbe for the Traffic Manger.                                                                               | `{}`                                                                        |
| resources                                            | Define resource requests and limits for the Traffic Manger.                                                                 | `{}`                                                                        |
| logLevel                                             | Define the logging level of the Traffic Manager                                                                             | `debug`                                                                     |
| timeouts.agentArrival                                | The time that the traffic-manager will wait for the traffic-agent to arrive                                                 | `30s`                                                                       |
| agent.appProtocolStrategy                            | The strategy to use when determining the application protocol to use for intercepts                                         | `http2Probe`                                                                |
| agent.logLevel                                       | The logging level for the traffic-agent                                                                                     | defaults to logLevel                                                        |
| agent.resources                                      | The resources for the injected agent container                                                                              |                                                                             |
| agent.initResources                                  | The resources for the injected init container                                                                               |                                                                             |
| agent.image.registry                                 | The registry for the injected agent image                                                                                   | `docker.io/datawire`                                                        |
| agent.image.name                                     | The name of the injected agent image                                                                                        | `""`                                                                        |
| agent.image.tag                                      | The tag for the injected agent image                                                                                        | `""` (Defined in `appVersion` Chart.yaml)                                   |
| agent.image.pullPolicy                               | Pull policy in the webhook for the traffic agent image                                                                      | `IfNotPresent`                                                              |
| agentInjector.name                                   | Name to use with objects associated with the agent-injector.                                                                | `agent-injector`                                                            |
| agentInjector.certificate.regenerate                 | Define whether you want to regenerate certificate used for mutating webhook.                                                | `false`                                                                     |
| agentInjector.certificate.method                     | Define which method to use to generate certificate used for mutating webhook. (helm of certmanager)                         | `helm`                                                                      |
| agentInjector.certificate.certmanager.commonName     | The common name of the generated Certmanager certificate.                                                                   | `agent-injector`                                                            |
| agentInjector.certificate.certmanager.duration       | The certificate validity duration. (optional value)                                                                         | `2160h0m0s`                                                                 |
| agentInjector.certificate.certmanager.issuerRef.name | The Issuer name to use to generate the self signed certificate.                                                             | `telepresence`                                                              |
| agentInjector.certificate.certmanager.issuerRef.kind | The Issuer kind to use to generate the self signed certificate. (Issuer of ClusterIssuer)                                   | `Issuer`                                                                    |
| agentInjector.injectPolicy                           | Determines when an agent is injected, possible values are `OnDemand` and `WhenEnabled`                                      | `OnDemand`                                                                  |
| agentInjector.service.type                           | Type of service for the agent-injector.                                                                                     | `ClusterIP`                                                                 |
| agentInjector.secret.name                            | The name of the secret the agent-injector webhook uses for authorization with the kubernetes api will expose.               | `mutator-webhook-tls`                                                       |
| agentInjector.webhook.name                           | The name of the agent-injector webhook                                                                                      | `agent-injector-webhook`                                                    |
| agentInjector.webhook.admissionReviewVersions:       | List of supported admissionReviewVersions.                                                                                  | `["v1"]`                                                                    |
| agentInjector.webhook.namespaceSelector:             | The namespaceSelector used by the agent-injector webhook when the traffic-manager is not namespaced.                        | namespaceSelector                                                           |
| agentInjector.webhook.servicePath:                   | Path to the service that provides the admission webhook                                                                     | `/traffic-agent`                                                            |
| agentInjector.webhook.port:                          | Port for the service that provides the admission webhook                                                                    | `443`                                                                       |
| agentInjector.webhook.reinvocationPolicy:            | Specify if the webhook may be called again after the initial webhook call. Possible values are `Never` and `IfNeeded`.      | `IfNeeded`                                                                  |
| agentInjector.webhook.failurePolicy:                 | Action to take on unexpected failure or timeout of webhook.                                                                 | `Ignore`                                                                    |
| agentInjector.webhook.sideEffects:                   | Any side effects the admission webhook makes outside of AdmissionReview.                                                    | `None`                                                                      |
| agentInjector.webhook.timeoutSeconds:                | Timeout of the admission webhook                                                                                            | `5`                                                                         |
| rbac.only                                            | Only create the RBAC resources and omit the traffic-manger.                                                                 | `false`                                                                     |
| clientRbac.create                                    | Create RBAC resources for non-admin users with this release.                                                                | `false`                                                                     |
| clientRbac.subjects                                  | The user accounts to tie the created roles to.                                                                              | `{}`                                                                        |
| clientRbac.namespaced                                | Restrict the users to specific namespaces.                                                                                  | `false`                                                                     |
| clientRbac.namespaces                                | The namespaces to give users access to.                                                                                     | `["ambassador"]`                                                            |
| managerRbac.create                                   | Create RBAC resources for traffic-manager with this release.                                                                | `true`                                                                      |
| managerRbac.namespaced                               | Whether the traffic manager should be restricted to specific namespaces                                                     | `false`                                                                     |
| managerRbac.namespaces                               | Which namespaces the traffic manager should be restricted to                                                                | `[]`                                                                        |
| telepresenceAPI.port                                 | The port on agent's localhost where the Telepresence API server can be found                                                |                                                                             |
| hooks.podSecurityContext                             | The Kubernetes SecurityContext for the chart hooks `Pod`                                                                    | `{}`                                                                        |
| hooks.securityContext                                | The Kubernetes SecurityContext for the chart hooks `Container`                                                              | securityContext                                                             |
| hooks.resources                                      | Define resource requests and limits for the chart hooks                                                                     | `{}`                                                                        |
| hooks.busybox.registry                               | The registry to download the image from.                                                                                    | `docker.io`                                                                 |
| hooks.busybox.image                                  | The name of the image to use for busybox.                                                                                   | `busybox`                                                                   |
| hooks.busybox.tag                                    | Override the version of busybox to be installed.                                                                            | `latest`                                                                    |
| hooks.busybox.imagePullSecrets                       | The `Secret` storing any credentials needed to access the image in a private registry.                                      | `[]`                                                                        |
| hooks.curl.registry                                  | The repository to download the image from.                                                                                  | `docker.io`                                                                 |
| hooks.curl.image                                     | The name of the image to use for curl.                                                                                      | `curlimages/curl`                                                           |
| hooks.curl.tag                                       | Override the version of busybox to be installed.                                                                            | `latest`                                                                    |
| hooks.curl.imagePullSecrets                          | The `Secret` storing any credentials needed to access the image in a private registry.                                      | `[]`                                                                        |
| client.connectionTTL                                 | The time that the traffic-manager will retain a client connection without any sign of life from the workstation             | `24h`                                                                       |
| client.routing.alsoProxySubnets                      | The virtual network interface of connected clients will also proxy these subnets                                            | `[]`                                                                        |
| client.routing.neverProxySubnets                     | The virtual network interface of connected clients never proxy these subnets                                                | `[]`                                                                        |
| client.routing.allowConflictingSubnets               | Allow the specified subnets to be routed even if they conflict with other routes on the local machine.                      | `[]`                                                                        |
| client.dns.excludeSuffixes                           | Suffixes for which the client DNS resolver will always fail (or fallback in case of the overriding resolver)                | `[".com", ".io", ".net", ".org", ".ru"]`                                    |
| client.dns.includeSuffixes                           | Suffixes for which the client DNS resolver will always attempt to do a lookup. Includes have higher priority than excludes. | `[]`                                                                        |

### RBAC

Telepresence requires a cluster for installation but restricted RBAC roles can
be used to give users access to create intercepts if they are not cluster
admins.

The chart gives you the ability to create these RBAC roles for your users and
give access to the entire cluster or restrict to certain namespaces.

You can also create a separate release for managing RBAC by setting
`Values.rbac.only: true`.

### Namespace-scoped traffic manager

Telepresence's Helm chart supports installing a Traffic Manager at the namespace scope.
You might want to do this if you have multiple namespaces, say representing multiple different environments, and would like their Traffic Managers to be isolated from one another.
To do this, set `managerRbac.namespaced=true` and `managerRbac.namespaces={a,b,c}` to manage namespaces `a`, `b` and `c`.

**NOTE** Do not install namespace-scoped traffic managers and a cluster-scoped traffic manager in the same cluster!

#### Namespace collision detection

The Telepresence Helm chart will try to prevent namespace-scoped Traffic Managers from managing the same namespaces.
It will do this by creating a ConfigMap, called `traffic-manager-claim`, in each namespace that a given install manages.

So, for example, suppose you install one Traffic Manager to manage namespaces `a` and `b`, as:

```bash
$ telepresence helm install --namespace a --set 'managerRbac.namespaced=true' --set 'managerRbac.namespaces={a,b}'
```

You might then attempt to install another Traffic Manager to manage namespaces `b` and `c`:

```bash
$ telepresence helm install --namespace c --set 'managerRbac.namespaced=true' --set 'managerRbac.namespaces={b,c}'
```

This would fail with an error:

```
Error: rendered manifests contain a resource that already exists. Unable to continue with install: ConfigMap "traffic-manager-claim" in namespace "b" exists and cannot be imported into the current release: invalid ownership metadata; annotation validation error: key "meta.helm.sh/release-namespace" must equal "c": current value is "a"
```

To fix this error, fix the overlap either by removing `b` from the first install, or from the second.

#### Pod CIDRs

The traffic manager is responsible for keeping track of what CIDRs the cluster uses for the pods. The Telepresence client uses this
information to configure the network so that it provides access to the pods. In some cases, the traffic-manager will not be able to retrieve
this information, or will do it in a way that is inefficient. To remedy this, the strategy that the traffic manager uses can be configured
using the `podCIDRStrategy`.

| Value          | Meaning                                                                                                                   |
| -------------- | ------------------------------------------------------------------------------------------------------------------------- |
| `auto`         | First try `nodePodCIDRs` and if that fails, try `coverPodIPs`                                                             |
| `nodePodCIDRs` | Obtain the CIDRs from the`podCIDR` and `podCIDRs` of all `Node` resource specifications.                                  |
| `coverPodIPs`  | Obtain all IPs from the `podIP` and `podIPs` of all `Pod` resource statuses and calculate the CIDRs needed to cover them. |
| `environment`  | Pick the CIDRs from the traffic manager's `POD_CIDRS` environment variable. Use `podCIDRs` to set that variable.          |
