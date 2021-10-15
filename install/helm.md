# Install with Helm

[Helm](https://helm.sh) is a package manager for Kubernetes that automates the release and management of software on Kubernetes. The Telepresence Traffic Manager can be installed via a Helm chart with a few simple steps.

**Note** that installing the Traffic Manager through Helm will prevent `telepresence connect` from ever upgrading it. If you wish to upgrade a Traffic Manager that was installed via the Helm chart, please see the steps [below](#upgrading-the-traffic-manager)

For more details on what the Helm chart installs and what can be configured, see the Helm chart [README](https://github.com/telepresenceio/telepresence/tree/release/v2/charts/telepresence).

## Before you begin

The Telepresence Helm chart is hosted by Ambassador Labs and published at `https://app.getambassador.io`.

Start by adding this repo to your Helm client with the following command:

```shell
helm repo add datawire  https://app.getambassador.io
helm repo update
```

## Install with Helm

When you run the Helm chart, it installs all the components required for the Telepresence Traffic Manager. 

1. If you are installing the Telepresence Traffic Manager **for the first time on your cluster**, create the `ambassador` namespace in your cluster:

   ```shell
   kubectl create namespace ambassador
   ```

2. Install the Telepresence Traffic Manager with the following command:

   ```shell
   helm install traffic-manager --namespace ambassador datawire/telepresence
   ```

### Install into custom namespace

The Helm chart supports being installed into any namespace, not necessarily `ambassador`. Simply pass a different `namespace` argument to `helm install`.
For example, if you wanted to deploy the traffic manager to the `staging` namespace:

```bash
helm install traffic-manager --namespace staging datawire/telepresence
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

See [the kubeconfig documentation](../../reference/config#manager) for more information.

### Upgrading the Traffic Manager.

Versions of the Traffic Manager Helm chart are coupled to the versions of the Telepresence CLI that they are intended for.
Thus, for example, if you wish to use Telepresence `v2.4.0`, you'll need to install version `v2.4.0` of the Traffic Manager Helm chart.

Upgrading the Traffic Manager is the same as upgrading any other Helm chart; for example, if you installed the release into the `ambassador` namespace, and you just wished to upgrade it to the latest version without changing any configuration values:

```shell
helm repo up
helm upgrade traffic-manager datawire/telepresence --reuse-values --namespace ambassador
```

If you want to upgrade the Traffic-Manager to a specific version, add a `--version` flag with the version number to the upgrade command. For example: `--version v2.4.1`

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

```bash
helm install traffic-manager --namespace staging datawire/telepresence -f ./values.yaml
```

**NOTE** Do not install namespace-scoped Traffic Managers and a global Traffic Manager in the same cluster, as it could have unexpected effects.

#### Namespace collision detection

The Telepresence Helm chart will try to prevent namespace-scoped Traffic Managers from managing the same namespaces.
It will do this by creating a ConfigMap, called `traffic-manager-claim`, in each namespace that a given install manages.

So, for example, suppose you install one Traffic Manager to manage namespaces `dev` and `staging`, as:

```bash
helm install traffic-manager --namespace dev datawire/telepresence --set 'managerRbac.namespaced=true' --set 'managerRbac.namespaces={dev,staging}'
```

You might then attempt to install another Traffic Manager to manage namespaces `staging` and `prod`:

```bash
helm install traffic-manager --namespace prod datawire/telepresence --set 'managerRbac.namespaced=true' --set 'managerRbac.namespaces={staging,prod}'
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

#### Namespace-scoped webhook

If you wish to use the traffic-manager's [mutating webhook](../../reference/cluster-config#mutating-webhook) with a namespace-scoped traffic manager, you will have to ensure that each namespace has an `app.kubernetes.io/name` label that is identical to its name:

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: staging
  labels:
    app.kubernetes.io/name: staging
```

You can also use `kubectl label` to add the label to an existing namespace, e.g.:

```shell
kubectl label namespace staging app.kubernetes.io/name=staging
```

This is required because the mutating webhook will use the name label to find namespaces to operate on.

**NOTE** This labelling happens automatically in kubernetes >= 1.21.

### Installing RBAC only

Telepresence Traffic Manager does require some [RBAC](../../reference/rbac/) for the traffic-manager deployment itself, as well as for users.
To make it easier for operators to introspect / manage RBAC separately, you can use `rbac.only=true` to
only create the rbac-related objects.
Additionally, you can use `clientRbac.create=true` and `managerRbac.create=true` to toggle which subset(s) of RBAC objects you wish to create.
