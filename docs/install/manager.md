# Install/Uninstall the Traffic Manager

Telepresence uses a traffic manager to send/recieve cloud traffic to the user. Telepresence uses [Helm](https://helm.sh) under the hood to install the traffic manager in your cluster. The `telepresence` binary embeds both `helm` and a helm-chart for a traffic-manager that is of the same version as the binary.

## Prerequisites

Before you begin, you need to have [Telepresence installed](../install/client).

If you are not the administrator of your cluster, you will need [administrative RBAC permissions](../reference/rbac#administrating-telepresence) to install and use Telepresence in your cluster.

In addition, you may need certain prerequisites depending on your cloud provider and platform.
See the [cloud provider installation notes](../install/cloud) for more.

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

Note that users of Telepresence will need to configure their kubeconfig to find this installation of the Traffic Manager:

```yaml
apiVersion: v1
clusters:
- cluster:
    server: https://127.0.0.1
    extensions:
    - name: telepresence.io
      extension:
        manager:
          namespace: staging
  name: example-cluster
```

See [the kubeconfig documentation](../reference/config#manager) for more information.

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

## RBAC

### Installing a namespace-scoped traffic manager

You might not want the Traffic Manager to have permissions across the entire kubernetes cluster, or you might want to be able to install multiple traffic managers per cluster (for example, to separate them by environment).
In these cases, the traffic manager supports being installed with a namespace scope, allowing cluster administrators to limit the reach of a traffic manager's permissions.

For example, suppose you want a Traffic Manager that only works on namespaces `dev` and `staging`.
To do this, create a `values.yaml` like the following:

```yaml
managerRbac:
  create: true
  namespaced: true
  namespaces:
  - dev
  - staging
```

This can then be installed via:

```shell
telepresence helm install --namespace staging -f ./values.yaml
```

**NOTE** Do not install namespace-scoped Traffic Managers and a global Traffic Manager in the same cluster, as it could have unexpected effects.

#### Namespace collision detection

The Telepresence Helm chart will try to prevent namespace-scoped Traffic Managers from managing the same namespaces.
It will do this by creating a ConfigMap, called `traffic-manager-claim`, in each namespace that a given install manages.

So, for example, suppose you install one Traffic Manager to manage namespaces `dev` and `staging`, as:

```bash
telepresence helm install --namespace dev --set 'managerRbac.namespaced=true' --set 'managerRbac.namespaces={dev,staging}'
```

You might then attempt to install another Traffic Manager to manage namespaces `staging` and `prod`:

```bash
telepresence helm install --namespace prod --set 'managerRbac.namespaced=true' --set 'managerRbac.namespaces={staging,prod}'
```

This would fail with an error:

```
Error: rendered manifests contain a resource that already exists. Unable to continue with install: ConfigMap "traffic-manager-claim" in namespace "staging" exists and cannot be imported into the current release: invalid ownership metadata; annotation validation error: key "meta.helm.sh/release-namespace" must equal "prod": current value is "dev"
```

To fix this error, fix the overlap either by removing `staging` from the first install, or from the second.

#### Namespace scoped user permissions

Optionally, you can also configure user rbac to be scoped to the same namespaces as the manager itself.
You might want to do this if you don't give your users permissions throughout the cluster, and want to make sure they only have the minimum set required to perform telepresence commands on certain namespaces.

Continuing with the `dev` and `staging` example from the previous section, simply add the following to `values.yaml` (make sure you set the `subjects`!):

```yaml
clientRbac:
  create: true

  # These are the users or groups to which the user rbac will be bound.
  # This MUST be set.
  subjects: {}
  # - kind: User
  #   name: jane
  #   apiGroup: rbac.authorization.k8s.io

  namespaced: true

  namespaces:
  - dev
  - staging
```

### Installing RBAC only

Telepresence Traffic Manager does require some [RBAC](../reference/rbac/) for the traffic-manager deployment itself, as well as for users.
To make it easier for operators to introspect / manage RBAC separately, you can use `rbac.only=true` to
only create the rbac-related objects.
Additionally, you can use `clientRbac.create=true` and `managerRbac.create=true` to toggle which subset(s) of RBAC objects you wish to create.
