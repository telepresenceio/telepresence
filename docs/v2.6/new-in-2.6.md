# What’s new in Telepresence 2.6.0?

## No more workload modifications
The most common way to install the telepresence sidecar (the agent) has been that the client would modify the workload (the deployment,
replicaset, or statefulset), and actually insert the needed container in its pod template. The alternative, to use the mutating webhook
(a.k.a. the agent injector) has been possible for some time, but considered a more advanced use-case, and not that commonly used.
In 2.6.0, the workload modifications approach is removed and everything relies solely on the mutating webhook. This brings a number
of advantages:

- Workflows like Argo-CD no longer break (because the workload now remains stable).
- Client doesn’t need RBAC rules that allow modification of workloads.
- The modification is faster and more reliable.
- The Telepresence code-base shrinks (or well, it will once we drop compatibility with older traffic-managers).
- Upgrading is much easier (helm chart hooks send requests to the agent-injector).
- Ability to control global updates from SystemA by sending messages to the traffic-manager (not implemented yet, but now easy to do).

## Agent configuration in ConfigMap
The old sidecar was configured using environment variables. Some copied from the intercepted container (there could only be one) and others
added by the installer. This approach was not suitable when the demands on the sidecar grew beyond just intercepting one port on one
container. Now, the sidecar is instead configured using a hierarchical ConfigMap entry, allowing for more complex structures. Each namespace
that contains sidecars have a “telepresence-agents” ConfigMap, with one entry for each intercepted workload. The ConfigMap is maintained by
the traffic-manager and new entries are added to it automatically when a client requests an intercept on a workflow that hasn’t already been
intercepted.

## Intercept multiple containers and ports
The sidecar is now capable of intercepting multiple containers and multiple ports on each container. As before, an intercepted port must be
a service port that is connected to a port in a container in the intercepted workload. The difference is that now there can be any number of
such connections, and the user can choose which ones to intercept. Even the OSS-sidecar can do this, but it’s limited to one intercept at a
time.

## Smarter agent
The OSS-sidecar is only capable of handling the “tcp” mechanism. It offers no “personal” intercepts. This still holds. What’s different is
that while the old “smart” agent was able to handle “http” intercepts only, the new one can handle both “http” and “tcp” intercepts. This
means that it can handle all use-cases. A user that isn’t logged in will default to “tcp” and thus still block every other attempt to
intercept the same container/pod combo on the intercepted workflow, but there’s no longer a need to reinstall the agent in order for it to
handle that user. In fact, once the smart agent has been installed, it can remain there forever.

## New intercept flow
### Flow of old-style intercept
- Client asks SystemA for the preferred agent image (if logged in).
- Client finds the workload.
- Client finds the service based on the workflow’s container. It fails unless a unique service/container can be found (user can assist by
  providing service name/service port).
- Client checks if the agent is present, and if not, alters the workload:
    - Rename the container port (and the corresponding port in probes).
    - Add the sidecar with the original port name.
    - The client applies the modified workload.
- The client requests an intercept activation from the traffic-manager.
- The client creates the necessary mounts, and optionally starts a process (or docker container).

### Flow of new-style intercept
- Client asks the traffic-manager to prepare the intercept.
- Traffic-manager asks SystemA for the preferred sidecar-image (once, the result is then cached).
- Traffic-manager finds the workload.
- Traffic-manager ensures that an existing sidecar configuration exists and is up-to-date.
- If a new configuration must be created or an existing config was altered:
    - Traffic-manager creates a config based on all possible service/container connections.
    - The config is stored in the “telepresence-agents” configmap.
    - A watcher of the configmap receives an event with the new configuration, and ensures that the corresponding workload is rolled out.
    - The mutating webhook receives events for each pod and injects a sidecar based on the configuration.
- Traffic-manager checks if the prepared intercept is unique enough to proceed. If not , the prepare-request returns an error and the client
  is asked to provide info about service and/or service-port.
- The client requests an intercept activation from the traffic-manager.
- The client creates the necessary mounts, and optionally starts a process (or docker container).
