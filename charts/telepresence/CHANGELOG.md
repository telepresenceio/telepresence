# Change Log

### 2.6.8 (TBD)

- Feature: The helm-chart now supports settings resources, securityContext and podSecurityContext for use with chart hooks.

### v2.3.3-rc.0

- Feature: Add AgentInjectorWebhook yaml files, newly introduced in 2.3.1.
- Feature: Include service account + ClusterRole + ClusterRoleBinding for the traffic-manager, since it now communicates directly with the cluster.
- Feature: add `priorityClassName` value.
- Feature: add `nodeSelector`, `affinity` and `tolerations`  values to post-upgrade-hook and pre-delete-hook jobs.
- Change: image.repository has been split into image.registry and image.name since the registry is now used in another field.
- Change: the `rbac` value has been changed to be `clientRbac`.
- Change: `service.ports` has been removed since those values are not able to be changed.

### v0.2.0

- Feature: Create RBAC resources from helm chart
- Feature: Update to the 2.3.0 image of Telepresence

### v0.1.0

- Feature: Add support for installing the Traffic Manager with Helm
