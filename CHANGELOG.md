# Changelog

### 2.3.0 TBD

- Feature: Telepresence is now installable via brew
- Feature: `telepresence version` now also includes the version of the currently running user daemon.
- Change: A TUN-device is used instead of firewall rules for routing outbound connections.
- Change: Outbound connections now use gRPC instead of ssh, and the traffic-manager no longer has a sshd running.
- Change: The traffic-agent no longer has a sshd running. Remote volume mounts use sshfs in slave mode, talking directly to sftp.
- Change: The local DNS now routes the name lookups to intercepted agents or traffic-manager.
- Change: The default log-level for the traffic-manager and the root-daemon was changed from "debug" to "info".
- Change: The command line is now statically-linked, so it is usable on systems with different libc's.
- Bugfix: Using --docker-run no longer fail to mount remote volumes when docker runs as root.
- Bugfix: Fixed a number of race conditions.
- Bugfix: Fix a crash when there is an error communicating with the traffic-manager about Ambassador Cloud.
- Bugfix: Fix a bug where sometimes when displaying or logging a timeout error it fails to determine which configurable timeout is responsible.

### 2.2.2 (May 17, 2021)

- Feature: Telepresence translates legacy Telepresence commands into viable Telepresence commands.
- Bugfix: Intercepts will only look for agents that are in the same namespace as the intercept.

### 2.2.1 (April 29, 2021)

- Bugfix: Improve `ambassador` namespace detection that was trying to create the namespace even when the namespace existed, which was an undesired RBAC escalation for operators.
- Bugfix: Telepresence will now no longer generate excessive traffic trying repeatedly to exchange auth tokens with Ambassador Cloud.  This could happen when upgrading from <2.1.4 if you had an expired `telepresence login` from before upgrading.
- Bugfix: `telepresence login` now correctly handles expired logins, just like all of the other subcommands.

### 2.2.0 (April 19, 2021)

- Feature: `telepresence intercept` now has the option `--docker-run` which will start a docker container with intercepted environment and volume mounts.
- Bugfix: `telepresence uninstall` can once again uninstall agents installed by older versions of Telepresence.
- Feature: Addition of `telepresence current-cluster-id` and `telepresence license` commands for using licenses with the Ambassador extension, primarily in air-gapped environments.

### 2.1.5 (April 12, 2021)

- Feature: When intercepting `--port` now supports specifying a service port or a service name.  Previously, only service name was supported.
- Feature: Intercepts using `--mechanism=http` now support mTLS.
- Bugfix: One of the log messages was using the incorrect variable, which led to misleading error messages on `telepresence uninstall`.
- Bugfix: Telepresence no longer generates port names longer than 15 characters.

### 2.1.4 (April 5, 2021)

- Feature: `telepresence status` has been enhanced to provide more information.  In particular, it now provides separate information on the daemon and connector processes, as well as showing login status.
- Feature: Telepresence now supports intercepting StatefulSets
- Change: Telepresence necessary RBAC has been refined to support StatefulSets and now requires "get,list,update" for StatefulSets
- Change: Telepresence no longer requires that port 1080 must be available.
- Change: Telepresence now makes use of refresh tokens to avoid requiring the user to manually log in so often.
- Bugfix: Fix race condition that occurred when intercepting a ReplicaSet while another pod was terminating in the same namespace (this fixes a transient test failure)
- Bugfix: Fix error when intercepting a ReplicaSet requires the containerPort to be hidden.
- Bugfix: `telepresence quit` no longer starts the daemon process just to shut it down.
- Bugfix: Telepresence no longer hangs the next time it's run after getting killed.
- Bugfix: Telepresence now does a better job of automatically logging in as necessary, especially with regard to expired logins.
- Bugfix: Telepresence was incorrectly looking across all namespaces for services when intercepting, but now it only looks in the given namespace.  This should prevent people from running into "Found multiple services" errors when services with the same selectors existed in other namespaces.

### 2.1.3 (March 29, 2021)

- Feature: Telepresence now supports intercepting ReplicaSets (that aren't owned by a Deployment)
- Change: The --deployment (-d) flag is now --workload (-w), as we start supporting more workloads than just Deployments
- Change: Telepresence necessary RBAC has changed and now requires "delete" for Pods and "get,list,update" for ReplicaSets
- Security: Upgrade to a newer OpenSSL, to address OpenSSL CVE-2021-23840.
- Bugfix: Connecting to Minikube/Hyperkit no longer fails intermittently.
- Bugfix: Telepresence will now make /var/run/secrets/kubernetes.io available when mounting remote volumes.
- Bugfix: Hiccups in the connection to the cluster will no longer cause the connector to shut down; it now retries properly.
- Bugfix: Fix a crash when binary dependencies are missing.
- Bugfix: You can now specify a service when doing an intercept (--service), this is useful if you have two services that select on the same labels (e.g. If using Argo Rollouts do deployments)

### 2.1.2 (March 19, 2021)

- Bugfix: Uninstalling agents now only happens once per deployment instead of once per agent.
- Bugfix: The list command no longer shows agents from namespaces that aren't mapped.
- Bugfix: IPv6 routes now work and don't prevent other pfctl rules being written in MacOS
- Bugfix: Pods with `hostname` and/or `subdomain` now get correct DNS-names and routes.
- Change: Service UID was added to InterceptSpec to better link intercepts and services.
- Feature: All timeouts can now be configured in a <user-config-dir>/telepresence/config.yml file

### 2.1.1 (March 12, 2021)

- Bugfix: When looking at the container to intercept, it will check if there's a better match before using a container without containerPorts.
- Bugfix: Telepresence will now map `kube-*` and `ambassador` namespaces by default.
- Bugfix: Service port declarations that lack a TargetPort field will now correctly default to using the Port field instead.
- Bugfix: Several DNS fixes.  Notably, introduce a fake "tel2-search" domain that gets replaced with a dynamic DNS search when queried, which fixes DNS for Docker with no `-net host`.
- Change: Improvements to how we report the requirements for volume mounts; notably, if the requirements are not met then it defaults to `--mount=false`.
- Change: There has been substantial code cleanup in the "connector" process.

### 2.1.0 (March 8, 2021)

- Feature: Support headless services (including ExternalName), which you can use if you used "Also Proxy" in telepresence 1.
- Feature: Preview URLs can now set a layer-5 hostname (TLS-SNI and HTTP "Host" header) that is different than the layer-3 hostname (IP-address/DNS-name) that is used to dial to the ingress.
- Feature: The Ingress info will now contain a layer-5 hostname that can be used for TLS-SLI and HTTP "Host" header when accessing a service.
- Feature: Users can choose which port to intercept when intercepting a service with multiple ports.
- Bugfix: Environment variables declared with `envFrom` in the app-container are now propagated correctly to the client during intercept.
- Bugfix: The description of the `--everything` flag for the `uninstall` command was corrected.
- Bugfix: Connecting to a large cluster could take a very long time and even make the process hang. This is no longer the case.
- Bugfix: Telepresence now explicitly requires macFUSE version 4.0.5 or higher for macOS.
- Bugfix: A `tail -F <daemon log file>` no longer results in a "Permission denied" when reconnecting to the cluster.
- Change: The telepresence daemon will no longer use port 1234 for the firewall-to-SOCKS server, but will instead choose an available port dynamically.
- Change: On connect, telepresence will no longer suggest the `--mapped-namespaces` flag when the user connects to a large cluster.

### 2.0.3 (February 24, 2021)

- Feature: There is now an extension mechanism where you can tell Telepresence about different agents and what arguments they support.  The new `--mechanism` flag can explicitly identify which extension to use.
- Feature: An intercept of `NAME` that is made using `--namespace=NAMESPACE` but not using `--deployment` will use `NAME` as the name of the deployment and `NAME-NAMESPACE` as the name of the intercept.
- Feature: Declare a local-only intercept for the purpose of getting direct outbound access to the intercept's namespace using boolean flag `--local-only`.
- Bugfix: Fix a regression in the DNS resolver that prevented name resolution using NAME.NAMESPACE. Instead, NAME.NAMESPACE.svc.cluster.local was required.
- Bugfix: Fixed race-condition in the agent causing attempts to dial to `:0`.
- Bugfix: It is now more strict about which agent versions are acceptable and will be more eager to apply upgrades.
- Change: Related to things now being in extensions, the `--match` flag has been renamed to `--http-match`.
- Change: Cluster connection timeout has been increased from 10s to 20s.
- Change: On connect, if telepresence detects a large cluster, it will suggest the `--mapped-namespaces` flag to the user as a way to speed it up.
- Change: The traffic-agent now has a readiness probe associated with its container.

### 2.0.2 (February 18, 2021)

- Feature: Telepresence is now capable of forwarding the intercepted Pod's volume mounts (as Telepresence 0.x did) via the `--mount` flag to `telepresence intercept`.
- Feature: Telepresence will now allow simultaneous intercepts in different namespaces.
- Feature: It is now possible for a user to limit what namespaces that will be used by the DNS-resolver and the NAT.
- Bugfix: Fix the kubectl version number check to handle version numbers with a "+" in them.
- Bugfix: Fix a bug with some configurations on macOS where we clash with mDNSResponder's use of port 53.

### 2.0.1 (February 9, 2021)

- Feature: Telepresence is now capable of forwarding the environment variables of an intercepted service (as Telepresence 0.x did) and emit them to a file as text or JSON. The environment variables will also be propagated to any command started by doing a `telepresence intercept nnn -- <command>`.
- Bugfix: A bug causing a failure in the Telepresence DNS resolver when attempting to listen to the Docker gateway IP was fixed. The fix affects Windows using a combination of Docker and WSL2 only.
- Bugfix: Telepresence now works correctly while OpenVPN is running on macOS.
- Change: The background processes `connector` and `daemon` will now use rotating logs and a common directory.
  + MacOS: `~/Library/Logs/telepresence/`
  + Linux: `$XDG_CACHE_HOME/telepresence/logs/` or `$HOME/.cache/telepresence/logs/`
