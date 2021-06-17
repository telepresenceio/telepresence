# Change Log

## v0.3.0
- Feature: Add AgentInjectorWebhook yaml files, newly introduced in 2.3.1.
- Feature: Include service account + ClusterRole + ClusterRoleBinding for the traffic-manager, since it now communicates directly with the cluster.
- Change: image.repository has been split into image.registry and image.name since the registry is now used in another field.
## v0.2.0

- Feature: Create RBAC resources from helm chart
- Feature: Update to the 2.3.0 image of Telepresence
## v0.1.0

- Feature: Add support for installing the Traffic Manager with Helm
