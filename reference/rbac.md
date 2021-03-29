# RBAC

## Necessary RBAC for Users

To use telepresence, users will need to have at least the following permissions:
```
- apiGroups:
  - ""
  resources: ["pods"]
  verbs: ["get", "list", "create", "watch", "delete"]
- apiGroups:
  - ""
  resources: ["services"]
  verbs: ["get", "list", "watch", "update"]
- apiGroups:
  - ""
  resources: ["pods/portforward"]
  verbs: ["create"]
- apiGroups:
  - "apps"
  resources: ["deployments", "replicasets"]
  verbs: ["get", "list", "update"]
- apiGroups:
  - "getambassador.io"
  resources: ["hosts", "mappings"]
  verbs: ["*"]
- apiGroups:
  - ""
  resources: ["endpoints"]
  verbs: ["get", "list", "watch"]
- apiGroups:
  - ""
  resources: ["namespaces"]
  verbs: ["get", "list", "watch"]
```
