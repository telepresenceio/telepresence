---
title: RBAC
toc_min_heading_level: 2
toc_max_heading_level: 2
---

# Telepresence RBAC
The intention of this document is to provide a template for securing and limiting the permissions of Telepresence.
This documentation covers the full extent of permissions necessary to administrate Telepresence components in a cluster.

There are two general categories for cluster permissions with respect to Telepresence.  There are RBAC settings for a User and for an Administrator described above.  The User is expected to only have the minimum cluster permissions necessary to create a Telepresence [engagement](../howtos/engage.md), and otherwise be unable to affect Kubernetes resources.

In addition to the above, there is also a consideration of how to manage Users and Groups in Kubernetes which is outside of the scope of the document.  This document will use Service Accounts to assign Roles and Bindings.  Other methods of RBAC administration and enforcement can be found on the [Kubernetes RBAC documentation](https://kubernetes.io/docs/reference/access-authn-authz/rbac/) page.

## Requirements

- Kubernetes version 1.16+
- Cluster admin privileges to apply RBAC

## Editing your kubeconfig

This guide also assumes that you are utilizing a kubeconfig file that is specified by the `KUBECONFIG` environment variable.  This is a `yaml` file that contains the cluster's API endpoint information as well as the user data being supplied for authentication.  The Service Account name used in the example below is called tp-user.  This can be replaced by any value (i.e. John or Jane) as long as references to the Service Account are consistent throughout the `yaml`.  After an administrator has applied the RBAC configuration, a user should create a `config.yaml` in your current directory that looks like the following:

```yaml
apiVersion: v1
kind: Config
clusters:
- name: my-cluster # Must match the cluster value in the contexts config
  cluster:
    ## The cluster field is highly cloud-dependent.
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

Telepresence administration requires permissions for creating the `traffic-manager` [deployment](architecture.md#traffic-manager) which is typically
done by a full cluster administrator.

Once installed, the Telepresence Traffic Manager will run using the `traffic-manager` ServiceAccount. This account is
set up differently depending on if the manager is installed using a dynamic or a static namespace selector.

### Installation without, or with dynamic, namespace selection

The Traffic Manager will require cluster wide access to several resources when it lacks a namespace selector, or when it
is configured with a dynamic namespace selector.

### Traffic Manager Permissions

These are the permissions required by the `traffic-manager` account in such a configuration:

```yaml
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: traffic-manager
  namespace: ambassador
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: traffic-manager
rules:
  - apiGroups: ["apps"]
    resources: ["deployments", "replicasets", "statefulsets"]
    verbs: ["get", "list", "watch"]
  - apiGroups: [""]
    resources: ["namespaces"]
    verbs: ["get", "list"]
  - apiGroups: ["events.k8s.io"]
    resources: ["events"]
    verbs: ["get", "watch"]
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["get", "list", "watch", "patch"] # patch not needed when agentInjector.enabled is set to false
  - apiGroups: ["networking.k8s.io"]
    resources: ["servicecidrs"]
    verbs: ["list"]
  
  # If argoRollouts.enabled is set to true
  - apiGroups: ["argoproj.io"]
    resources: ["rollouts"]
    verbs: ["get", "list", "watch"]

  # When using podCIDRStrategy nodePodCIDRs
  - apiGroups: [""]
    resources: ["nodes"]
    verbs: ["get", "list", "watch"]

  # The following is not needed when agentInjector.enabled is set to false
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["patch"]
  - apiGroups: ["apps"]
    resources: ["deployments", "replicasets", "statefulsets"]
    verbs: ["patch"]
  # If argoRollouts.enabled is set to true
  - apiGroups: ["argoproj.io"]
    resources: ["rollouts"]
    verbs: ["patch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: traffic-manager
subjects:
  - name: traffic-manager
    kind: ServiceAccount
    namespace: default
roleRef:
  apiGroup: rbac.authorization.k8s.io
  name: traffic-manager
  kind: ClusterRole
```


### Installation with static namespace selection

The permissions required by the `traffic-manager` account in a statically namespaced configuration is very similar to
the ones used in a dynamic configuration, but a `Role`/`RoleBinding` will be installed in each managed namespace instead
of the `ClusterRole`/`ClusterRoleBinding` pair.

> [!NOTE]
> One `ClusterRole/ClusterRoleBinding` will still be present that permits the traffic-manager to list the `servicecidr` resource.
> That resource is cluster wide, so the following cluster wide rule is required:
>
> ```yaml
>   - apiGroups: ["networking.k8s.io"]
>     resources: ["servicecidrs"]
>     verbs: ["list"]
> ```

## Telepresence Client Access

A Telepresence client requires just a small set of RBAC permissions. The bare minimum to connect is the ability to
create a port-forward to the traffic-manager.

The following configuration assumes that a ServiceAccount "tp-user" has been created in the traffic-manager's default
"ambassador" namespace.

In order to connect, the client must resolve the traffic-manager service name into a pod-IP and set up a port-forward.
This requires the following Role/RoleBinding in the Traffic Manager's namespace.

```yaml
kind: Role
apiVersion: rbac.authorization.k8s.io/v1
metadata:
  name:  traffic-manager-connect
  namespace: ambassador
rules:
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["get", "list"]
  - apiGroups: [""]
    resources: ["services"]
    resourceNames: ["traffic-manager"]
    verbs: ["get"]
  - apiGroups: [""]
    resources: ["pods/portforward"]
    verbs: ["create"]
---

apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: traffic-manager-connect
  namespace: ambassador
subjects:
  - kind: ServiceAccount
    name: tp-user
    namespace: ambassador
roleRef:
  kind: Role
  name: traffic-manager-connect
  apiGroup: rbac.authorization.k8s.io
```

Once connected, it is desirable, but not necessary that the client can create port-forwards directly to Traffic Agents
in the namespace that it is connected to. The lack of this permission will cause all traffic to be routed via the
Traffic Manager, which will have a slightly negative impact on throughput.

It's recommended that the client also has the following permissions in a dynamic namespaces installation:

```yaml
kind: ClusterRole
apiVersion: rbac.authorization.k8s.io/v1
metadata:
  name:  telepresence-ambassador
rules:
- apiGroups:
  - ""
  resources: ["namespaces"]
  verbs: ["get", "list", "watch"]
- apiGroups: [""]
  resources: ["pods"]
  verbs: ["get"]

  # Necessary if the client should be able to gather the pod logs
- apiGroups: [""]
  resources: ["pods"]
  verbs: ["list"]
- apiGroups: [""]
  resources: ["pods/log"]
  verbs: ["get"]

  # All traffic will be routed via the traffic-manager unless a portforward can be created directly to a pod
- apiGroups: [""]
  resources: ["pods/portforward"]
  verbs: ["create"]

---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: telepresence-ambassador
subjects:
  - kind: ServiceAccount
    name: tp-user
    namespace: ambassador
roleRef:
  kind: ClusterRole
  name: telepresence-ambassador
  apiGroup: rbac.authorization.k8s.io
```

The corresponding configuration for a static namespace installation, for each namespaece that the client should be able
to access:


```yaml
kind: Role
apiVersion: rbac.authorization.k8s.io/v1
metadata:
  name:  telepresence-client
rules:
- apiGroups: [""]
  resources: ["pods"]
  verbs: ["get"]

  # Necessary if the client should be able to gather the pod logs
- apiGroups: [""]
  resources: ["pods"]
  verbs: ["list"]
- apiGroups: [""]
  resources: ["pods/log"]
  verbs: ["get"]

  # All traffic will be routed via the traffic-manager unless a portforward can be created directly to a pod
- apiGroups: [""]
  resources: ["pods/portforward"]
  verbs: ["create"]

---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: telepresence-client
subjects:
  - kind: ServiceAccount
    name: tp-user
    namespace: ambassador
roleRef:
  kind: Role
  name: telepresence-client
  apiGroup: rbac.authorization.k8s.io
```

The user will also need the [Traffic Manager connect permission](#traffic-manager-permissions) described above.
