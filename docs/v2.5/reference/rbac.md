import Alert from '@material-ui/lab/Alert';

# Telepresence RBAC
The intention of this document is to provide a template for securing and limiting the permissions of Telepresence.
This documentation covers the full extent of permissions necessary to administrate Telepresence components in a cluster.

There are two general categories for cluster permissions with respect to Telepresence.  There are RBAC settings for a User and for an Administrator described above.  The User is expected to only have the minimum cluster permissions necessary to create a Telepresence [intercept](../../howtos/intercepts/), and otherwise be unable to affect Kubernetes resources.

In addition to the above, there is also a consideration of how to manage Users and Groups in Kubernetes which is outside of the scope of the document.  This document will use Service Accounts to assign Roles and Bindings.  Other methods of RBAC administration and enforcement can be found on the [Kubernetes RBAC documentation](https://kubernetes.io/docs/reference/access-authn-authz/rbac/) page.

## Requirements

- Kubernetes version 1.16+
- Cluster admin privileges to apply RBAC

## Editing your kubeconfig

This guide also assumes that you are utilizing a kubeconfig file that is specified by the `KUBECONFIG` environment variable.  This is a `yaml` file that contains the cluster's API endpoint information as well as the user data being supplied for authentication.  The Service Account name used in the example below is called tp-user.  This can be replaced by any value (i.e. John or Jane) as long as references to the Service Account are consistent throughout the `yaml`.  After an administrator has applied the RBAC configuration, a user should create a `config.yaml` in your current directory that looks like the following:​

```yaml
apiVersion: v1
kind: Config
clusters:
- name: my-cluster # Must match the cluster value in the contexts config
  cluster:
    ## The cluster field is highly cloud dependent.
contexts:
- name: my-context
  context:
    cluster: my-cluster # Must match the name field in the clusters config
    user: tp-user
users:
- name: tp-user # Must match the name of the Service Account created by the cluster admin
  user:
    token: <service-account-token> # See note below
```

The Service Account token will be obtained by the cluster administrator after they create the user's Service Account.  Creating the Service Account will create an associated Secret in the same namespace with the format `<service-account-name>-token-<uuid>`.  This token can be obtained by your cluster administrator by running `kubectl get secret -n ambassador <service-account-secret-name> -o jsonpath='{.data.token}' | base64 -d`.

After creating `config.yaml` in your current directory, export the file's location to KUBECONFIG by running `export KUBECONFIG=$(pwd)/config.yaml`.  You should then be able to switch to this context by running `kubectl config use-context my-context`.

## Administrating Telepresence

Telepresence administration requires permissions for creating `Namespaces`, `ServiceAccounts`, `ClusterRoles`, `ClusterRoleBindings`, `Secrets`, `Services`, `MutatingWebhookConfiguration`, and for creating the `traffic-manager` [deployment](../architecture/#traffic-manager) which is typically done by a full cluster administrator. The following permissions are needed for the installation and use of Telepresence:

```yaml
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: telepresence-admin
  namespace: default
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: telepresence-admin-role
rules:
  - apiGroups:
      - ""
    resources: ["pods", "pods/log"]
    verbs: ["get", "list", "create", "delete", "watch"]
  - apiGroups:
      - ""
    resources: ["services"]
    verbs: ["get", "list", "update", "create", "delete"]
  - apiGroups:
      - ""
    resources: ["pods/portforward"]
    verbs: ["create"]
  - apiGroups:
      - "apps"
    resources: ["deployments", "replicasets", "statefulsets"]
    verbs: ["get", "list", "update", "create", "delete", "watch"]
  - apiGroups:
      - "getambassador.io"
    resources: ["hosts", "mappings"]
    verbs: ["*"]
  - apiGroups:
      - ""
    resources: ["endpoints"]
    verbs: ["get", "list"]
  - apiGroups:
      - "rbac.authorization.k8s.io"
    resources: ["clusterroles", "clusterrolebindings", "roles", "rolebindings"]
    verbs: ["get", "list", "watch", "create", "delete"]
  - apiGroups:
      - ""
    resources: ["namespaces"]
    verbs: ["get", "list", "watch", "create"]
  - apiGroups:
      - ""
    resources: ["secrets"]
    verbs: ["get", "create", "list", "delete"]
  - apiGroups:
      - ""
    resources: ["serviceaccounts"]
    verbs: ["get", "create", "delete"]
  - apiGroups:
      - "admissionregistration.k8s.io"
    resources: ["mutatingwebhookconfigurations"]
    verbs: ["get", "create", "delete"]
  - apiGroups:
      - ""
    resources: ["nodes"]
    verbs: ["list", "get", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: telepresence-clusterrolebinding
subjects:
  - name: telepresence-admin
    kind: ServiceAccount
    namespace: default
roleRef:
  apiGroup: rbac.authorization.k8s.io
  name: telepresence-admin-role
  kind: ClusterRole
```

There are two ways to install the traffic-manager: Using `telepresence connect` and installing the [helm chart](../../install/helm/).

By using `telepresence connect`, Telepresence will use your kubeconfig to create the objects mentioned above in the cluster if they don't already exist.  If you want the most introspection into what is being installed, we recommend using the helm chart to install the traffic-manager.

## Cluster-wide telepresence user access

To allow users to make intercepts across all namespaces, but with more limited `kubectl` permissions, the following `ServiceAccount`, `ClusterRole`, and `ClusterRoleBinding` will allow full `telepresence intercept` functionality.

<Alert severity="warning">The following RBAC configurations assume that there is already a Traffic Manager deployment set up by a Cluster Administrator</Alert>

```yaml
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: tp-user                                       # Update value for appropriate value
  namespace: ambassador                                # Traffic-Manager is deployed to Ambassador namespace
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: telepresence-role
rules:
- apiGroups:
  - ""
  resources: ["pods", "pods/log"]
  verbs: ["get", "list", "create", "delete"]
- apiGroups:
  - ""
  resources: ["services"]
  verbs: ["get", "list", "update", "watch"]
- apiGroups:
  - ""
  resources: ["pods/portforward"]
  verbs: ["create"]
- apiGroups:
  - "apps"
  resources: ["deployments", "replicasets", "statefulsets"]
  verbs: ["get", "list", "update", "patch", "watch"]
- apiGroups:
  - "getambassador.io"
  resources: ["hosts", "mappings"]
  verbs: ["*"]
- apiGroups:
  - ""
  resources: ["endpoints"]
  verbs: ["get", "list"]
- apiGroups:
  - "rbac.authorization.k8s.io"
  resources: ["clusterroles", "clusterrolebindings"]
  verbs: ["get", "list", "watch"]
- apiGroups:
  - ""
  resources: ["namespaces"]
  verbs: ["get", "list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: telepresence-rolebinding
subjects:
- name: tp-user
  kind: ServiceAccount
  namespace: ambassador
roleRef:
  apiGroup: rbac.authorization.k8s.io
  name: telepresence-role
  kind: ClusterRole
```

## Namespace only telepresence user access

RBAC for multi-tenant scenarios where multiple dev teams are sharing a single cluster where users are constrained to a specific namespace(s).

<Alert severity="warning">The following RBAC configurations assume that there is already a Traffic Manager deployment set up by a Cluster Administrator</Alert>

```yaml
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: tp-user                                       # Update value for appropriate user name
  namespace: ambassador                                # Traffic-Manager is deployed to Ambassador namespace
---
kind: ClusterRole
apiVersion: rbac.authorization.k8s.io/v1
metadata:
  name:  telepresence-role
rules:
- apiGroups:
  - ""
  resources: ["pods"]
  verbs: ["get", "list", "create", "watch", "delete"]
- apiGroups:
  - ""
  resources: ["services"]
  verbs: ["update"]
- apiGroups:
  - ""
  resources: ["pods/portforward"]
  verbs: ["create"]
- apiGroups:
  - "apps"
  resources: ["deployments", "replicasets", "statefulsets"]
  verbs: ["get", "list", "update", "watch"]
- apiGroups:
  - "getambassador.io"
  resources: ["hosts", "mappings"]
  verbs: ["*"]
- apiGroups:
  - ""
  resources: ["endpoints"]
  verbs: ["get", "list", "watch"]
---
kind: RoleBinding                                      # RBAC to access ambassador namespace
apiVersion: rbac.authorization.k8s.io/v1
metadata:
  name: t2-ambassador-binding
  namespace: ambassador
subjects:
- kind: ServiceAccount
  name: tp-user                                       # Should be the same as metadata.name of above ServiceAccount
  namespace: ambassador
roleRef:
  kind: ClusterRole
  name: telepresence-role
  apiGroup: rbac.authorization.k8s.io
---
kind: RoleBinding                                      # RoleBinding T2 namespace to be intecpeted
apiVersion: rbac.authorization.k8s.io/v1
metadata:
  name: telepresence-test-binding                      # Update "test" for appropriate namespace to be intercepted
  namespace: test                                      # Update "test" for appropriate namespace to be intercepted
subjects:
- kind: ServiceAccount
  name: tp-user                                       # Should be the same as metadata.name of above ServiceAccount
  namespace: ambassador
roleRef:
  kind: ClusterRole
  name: telepresence-role
  apiGroup: rbac.authorization.k8s.io
​
---
kind: ClusterRole
apiVersion: rbac.authorization.k8s.io/v1
metadata:
  name:  telepresence-namespace-role
rules:
- apiGroups:
  - ""
  resources: ["namespaces"]
  verbs: ["get", "list", "watch"]
- apiGroups:
  - ""
  resources: ["services"]
  verbs: ["get", "list", "watch"]
---
kind: ClusterRoleBinding
apiVersion: rbac.authorization.k8s.io/v1
metadata:
  name: telepresence-namespace-binding
subjects:
- kind: ServiceAccount
  name: tp-user                                       # Should be the same as metadata.name of above ServiceAccount
  namespace: ambassador
roleRef:
  kind: ClusterRole
  name: telepresence-namespace-role
  apiGroup: rbac.authorization.k8s.io
```
