# Changelog

### 2.1.2 (TBD)
- Bugfix: Uninstalling agents now only happens once per deployment instead of once per agent.
- Bugfix: The list command no longer shows agents from namespaces that aren't mapped.

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
