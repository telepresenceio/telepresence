# Connecting to the cluster

## Setting up the proxy

To use Telepresence with a cluster (Kubernetes or OpenShift, local or remote) you need to run a proxy inside the cluster.
There are three ways of doing so.

### Creating a new deployment

By using the `--new-deployment` option `telepresence` can create a new deployment for you.
It will be deleted when the local `telepresence` process exits.
This is the default if no deployment option is specified.

For example, this creates a `Deployment` called `myserver`:

```console
telepresence --new-deployment myserver --run-shell
```

This will create two Kubernetes objects, a `Deployment` and a `Service`, both named `myserver`.
(On OpenShift a `DeploymentConfig` will be used instead of `Deployment`.)
Or, if you don't care what your new `Deployment` is called, you can do:

```console
telepresence --run-shell
```

If `telepresence` crashes badly enough (e.g. you used `kill -9`) you will need to manually delete the `Deployment` and `Service` that Telepresence created.

### Swapping out an existing deployment

If you already have your code running in the cluster you can use the `--swap-deployment` option to replace the existing deployment with the Telepresence proxy.
When the `telepresence` process exits it restores the earlier state of the `Deployment` (or `DeploymentConfig` on OpenShift).

```console
telepresence --swap-deployment myserver --run-shell
```

If you have more than one container in the pods created by the deployment you can also specify the container name:

```console
telepresence --swap-deployment myserver:containername --run-shell
```

If `telepresence` crashes badly enough (e.g. you used `kill -9`) you will need to manually restore the `Deployment`.


### Running Telepresence manually

You can also choose to run the Telepresence manually by starting a `Deployment` that runs the proxy in a pod.

The `Deployment` should only have 1 replica, and use the Telepresence different image:

<pre><code class="lang-yaml">apiVersion: extensions/v1beta1
kind: Deployment
metadata:
  name: myservice
spec:
  replicas: 1  # only one replica
  template:
    metadata:
      labels:
        name: myservice
    spec:
      containers:
      - name: myservice
        image: datawire/telepresence-k8s:{{ book['version'] }}  # new image
</code></pre>

You should apply this file to your cluster:

```console
kubectl apply -f telepresence-deployment.yaml
```

Next, you need to run the local Telepresence client on your machine, using `--deployment` to indicate the name of the `Deployment` object whose pod is running `telepresence/datawire-k8s`:

```console
telepresence --deployment myservice --run-shell
```

Telepresence will leave the deployment untouched when it exits.


## Kubernetes contexts and namespaces

### kubectl context

By default Telepresence uses whatever the current context is for `kubectl`.
If you want to choose a specific context you can use the `--context` option to `telepresence`.
For example:

```console
telepresence --context minikube --run-shell
```

You can choose any context listed in `kubectl config get-contexts`.

If you've [set a namespace for the context](https://kubernetes.io/docs/concepts/overview/working-with-objects/namespaces/#setting-the-namespace-preference) then that namespace will be used to find/create the `Deployment`, but you can also choose a namespace explicitly, as shown in the next section.

### Kubernetes namespaces

If you want to proxy to a Deployment in a non-default namespace you can pass the `--namespace` argument to Telepresence:

```console
telepresence --namespace yournamespace --swap-deployment yourservice --run-shell
```


## Cluster permissions

Telepresence uses `kubectl` or `oc` to manipulate your Kubernetes/OpenShift cluster.
This means the user who invokes Telepresence needs the appropriate authorization. For Kubernetes, the following Role should be sufficient:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: telepresence-role
  namespace: restricted-ns
rules:
- apiGroups: [""]
  resources: ["services"]
  verbs: ["list", "create", "delete"]
- apiGroups: ["", "apps", "extensions"]
  resources: ["deployments"]
  verbs: ["list", "create", "get", "update", "delete"]
- apiGroups: ["", "apps", "extensions"]
  resources: ["deployments/scale"]
  verbs: ["get", "update", "patch"]
- apiGroups: ["", "apps", "extensions"]
  resources: ["replicasets"]
  verbs: ["list", "get", "update", "delete"]
- apiGroups: [""]
  resources: ["pods"]
  verbs: ["list", "get", "create"]
- apiGroups: [""]
  resources: ["pods/log"]
  verbs: ["get"]
- apiGroups: [""]
  resources: ["pods/portforward"]
  verbs: ["create"]
- apiGroups: [""]
  resources: ["pods/exec"]
  verbs: ["create"]
```
