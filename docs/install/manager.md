---
title: Install Traffic Manager
hide_table_of_contents: true
---

# Install/Uninstall the Traffic Manager

Telepresence uses a traffic manager to send/receive cloud traffic to the user. Telepresence uses [Helm](https://helm.sh) under the
hood to install the traffic manager in your cluster. The `telepresence` binary embeds both `helm` and a helm-chart for a
traffic-manager that is of the same version as the binary.

The Telepresence Helm chart documentation is published at [ArtifactHUB](https://artifacthub.io/packages/helm/telepresence-oss/telepresence-oss).

You can also use `helm` command directly, see [Install With Helm](#install-with-helm) for more details.

## Prerequisites

Before you begin, you need to have [Telepresence installed](../install/client.md).

If you are not the administrator of your cluster, you will need [administrative RBAC permissions](../reference/rbac.md#administrating-telepresence) to install and use Telepresence in your cluster.

In addition, you may need certain prerequisites depending on your cloud provider and platform.
See the [cloud provider installation notes](../install/cloud.md) for more.

## Install the Traffic Manager

The telepresence cli can install the traffic manager for you. The basic install will install the same version as the client used.

1. Install the Telepresence Traffic Manager with the following command:

   ```shell
   telepresence helm install
   ```

### Customizing the Traffic Manager.

For details on what the Helm chart installs and what can be configured, see the Helm chart [configuration on artifacthub](https://artifacthub.io/packages/helm/datawire/telepresence).

1. Create a values.yaml file with your config values.

2. Run the `install` command with the `--values` flag set to the path to your values file:

   ```shell
   telepresence helm install --values values.yaml
   ```
   alternatively, provide values using the `--set` flag:
   ```shell
   telepresence helm install --set logLevel=debug
   ```

### Install into custom namespace

The Helm chart supports being installed into any namespace, not necessarily `ambassador`. Simply pass a different `namespace` argument to
`telepresence helm install`.  For example, if you wanted to deploy the traffic manager to the `staging` namespace:

```shell
telepresence helm install traffic-manager --namespace staging datawire/telepresence
```

> [!NOTE]
> If you have several traffic-managers installed, or if users don't have permissions to list
> namespaces, they will need to either use a `--manager-namespace <namespace>` flag when connecting or
> configure their config.yml or kubeconfig to find the desired installation of the Traffic Manager

As kubeconfig extension:
```yaml
apiVersion: v1
clusters:
- cluster:
    server: https://127.0.0.1
    extensions:
    - name: telepresence.io
      extension:
        cluster:
          defaultManagerNamespace: staging
  name: example-cluster
```

or in the `config.yml`:

```yaml
cluster:
 defaultManagerNamespace: staging
```

See [the kubeconfig documentation](../reference/config.md#manager) for more information.

## Upgrading/Downgrading the Traffic Manager.

1. Download the cli of the version of Telepresence you wish to use.

2. Run the `upgrade` command. Optionally with `--values` and/or `--set` flags 

   ```shell
   telepresence helm upgrade
   ```
   You can also use the `--reuse-values` or `--reset-values` to specify if previously installed values should be reused or reset.


## Uninstall

The telepresence cli can uninstall the traffic manager for you using the `telepresence helm uninstall`.

1. Uninstall the Telepresence Traffic Manager and all the agents installed by it using the following command:

   ```shell
   telepresence helm uninstall
   ```
## Limiting the Namespace Scope

You might not want the Traffic Manager to have permissions across the entire kubernetes cluster, or you might want to be able to install multiple traffic managers per cluster (for example, to separate them by environment).
In these cases, the traffic manager supports being installed with a namespace scope, allowing cluster administrators to limit the reach of a traffic manager's permissions.

For example, suppose you want a Traffic Manager that only works on namespaces `dev` and `staging`.
To do this, create a `values.yaml` like the following:

```yaml
namespaces:
  - dev
  - staging
```

This can then be installed via:

```shell
telepresence helm install --namespace staging -f ./values.yaml
```

### Namespace collision detection

The Telepresence Helm chart incorporates a mechanism to prevent conflicts between Traffic Managers operating within
different namespaces. This is achieved by:
1. Determining the Traffic Manager's set of namespaces by applying its namespace selector to all of the cluster's namespaces.
2. Verifying that there is no overlap between the sets of namespaces for any pair of Traffic Managers.

So, for example, suppose you install one Traffic Manager to manage namespaces `dev` and `staging`, as:

```bash
telepresence helm install --namespace dev --set 'namespaces={dev,staging}'
```

You might then attempt to install another Traffic Manager to manage namespaces `staging` and `prod`:

```bash
telepresence helm install --namespace prod --set 'namespaces={staging,prod}'
```

This would fail with an error:

```
telepresence helm install: error: execution error at (telepresence-oss/templates/agentInjectorWebhook.yaml:61:14): traffic-manager in namespace dev already manages namespace staging
```

To fix this error, fix the overlap either by removing `staging` from the first install, or from the second.

### Static versus Dynamic Namespace Selection

A namespace selector can be dynamic or static. This in turn controls if telepresence needs "cluster-wide" or
"namespaced" role/rolebinding pairs. A Traffic Manager configured with a dynamic selector requires cluster-wide
namespace access and `ClusterRole`/`ClusterRoleBinding` pairs. A Traffic Manager configured with a static selector needs
a `Role`/`RoleBinding` pair in each of the selected namespaces.

A selector is considered _static_ if it meets the following conditions:
- The selector must have exactly one element in either the `matchLabels` or the `matchExpression` list (a `key=value`
  element in the `matchLabels` list, it is normalized into a `key in [value]` expression element).
- The element must meet the following criteria:
  The `key` of the match expression must be "kubernetes.io/metadata.name".
  The `operator` of the match expression must be "In" (case sensitive).
  The `values` list of the match expression must contain at least one value.

## Static Namespace Selection RBAC

Optionally, you can also configure user rbac to be scoped to the same namespaces as the manager itself.
You might want to do this if you don't give your users permissions throughout the cluster, and want to make sure they
only have the minimum set required to perform telepresence commands on certain namespaces.

Continuing with the `dev` and `staging` example from the previous section, simply add the following to `values.yaml`
(make sure you set the `subjects`!):

```yaml
clientRbac:
  create: true

  # These are the users or groups to which the user rbac will be bound.
  # This MUST be set.
  subjects: {}
  # - kind: User
  #   name: jane
  #   apiGroup: rbac.authorization.k8s.io

  # The namespaces can be explicitly specified here, but can be omitted unless the
  # Traffic Manager's namespaceSelector is dynamic.
  namespaces:
  - dev
  - staging
```

### Installing RBAC only

Telepresence Traffic Manager does require some [RBAC](../reference/rbac.md) for the traffic-manager deployment itself, as well as for users.
To make it easier for operators to introspect / manage RBAC separately, you can use `rbac.only=true` to
only create the rbac-related objects.
Additionally, you can use `clientRbac.create=true` and `managerRbac.create=true` to toggle which subset(s) of RBAC objects you wish to create.

## Install with Helm

Before you begin, you must ensure that the [helm command](https://helm.sh/docs/intro/install/) is installed.

### Recommended version
In general, we recommend that you use a helm binary with a minor version that is less than three numbers below the version
embedded in the telepresence binary. E.g., for embedded version 3.18.x, use helm >= 3.16.x.

You can check your helm version using:
```bash
helm version
```
and the Telepresence embedded helm version using:
```bash
telepresence helm version
```

The Telepresence Helm chart is published at GitHub in the ghcr.io repository.

### Installing

Install the latest stable version of the traffic-manager into the default "ambassador" namespace with the following command:

```bash
helm install --create-namespace --namespace ambassador traffic-manager oci://ghcr.io/telepresenceio/telepresence-oss
```

### Upgrading/Downgrading

Use this command if you installed the Traffic Manager into the "ambassador" namespace, and you just wish to upgrade it
to the latest version without changing any configuration values:

```bash
helm upgrade --namespace ambassador --reuse-values traffic-manager oci://ghcr.io/telepresenceio/telepresence-oss
```

If you want to upgrade (or downgrade) the Traffic Manager to a specific version, add a `--version` flag with the version
number to the upgrade command, e.g.: `--version v2.20.3`.

### Uninstalling

Use the following command to uninstall the Traffic Manager:
```bash
helm uninstall --namespace ambassador traffic-manager
```
