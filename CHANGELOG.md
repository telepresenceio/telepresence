# Changelog

### 2.4.10 (TBD)

- Feature: The flag `--http-plaintext` can be used to ensure that an intercept uses plaintext http or grpc when 
  communicating with the workstation process.

- Feature: The port used by default in the `telepresence intercept` command (8080), can now be changed by setting
  the `intercept.defaultPort` in the `config.yml` file.

- Feature: The strategy when selecting the application protocol for personal intercepts in agents injected by the 
  mutating webhook can now be configured using the `agentInjector.appProtocolStrategy` in the Helm chart.

- Feature: The strategy when selecting the application protocol for personal intercepts can now be configured using
  the `intercept.appProtocolStrategy` in the `config.yml` file.

- Change: Telepresence CI now runs in Github Actions instead of Circle CI.

- Bugfix: Telepresence will no longer log invalid: "unhandled connection control message: code DIAL_OK" errors.

- Bugfix: User will not be asked to log in or add ingress information when creating an intercept until a check has been 
  made that the intercept is possible.

- Bugfix: Output to `stderr` from the traffic-agent's `sftp` and the client's `sshfs` processes are properly logged as errors.

- Bugfix: Auto installer will no longer not emit backslash separators for the `/tel-app-mounts` paths in the
  traffic-agent container spec when running on Windows

### 2.4.9 (December 9, 2021)

- Bugfix: Fixed an error where access tokens were not refreshed if you login
  while the daemons are already running.

- Bugfix: A helm upgrade using the --reuse-values flag no longer fails on a "nil pointer" error caused by a nil `telpresenceAPI` value.

### 2.4.8 (December 3, 2021)

- Feature: A RESTful service was added to Telepresence, both locally to the client and to the `traffic-agent` to help determine if messages with a set of headers should be
  consumed or not from a message queue where the intercept headers are added to the messages.

- Change: The environment variable TELEPRESENCE_LOGIN_CLIENT_ID is no longer used.

- Feature: There is a new subcommand, `test-vpn`, that can be used to diagnose connectivity issues with a VPN.

- Bugfix: The tunneled network connections between Telepresence and
  Ambassador Cloud now behave more like ordinary TCP connections,
  especially around timeouts.

### 2.4.7 (November 24, 2021)

- Feature: The agent injector now supports a new annotation, `telepresence.getambassador.io/inject-service-name`, that can be used to set the name of the service to be intercepted.
  This will help disambiguate which service to intercept for when a workload is exposed by multiple services, such as can happen with Argo Rollouts

- Feature: The kubeconfig extensions now support a `never-proxy` argument, analogous to `also-proxy`, that defines a set of subnets that will never be proxied via telepresence.

- Feature: Added flags to "telepresence intercept" that set the ingress fields as an alternative to using the dialogue.

- Change: Telepresence check the versions of the client and the daemons and ask the user to quit and restart if they don't match.

- Change: Telepresence DNS now uses a very short TTL instead of explicitly flushing DNS by killing the `mDNSResponder` or doing `resolvectl flush-caches`

- Bugfix: Legacy flags such as `--swap-deployment` can now be used together with global flags.

- Bugfix: Outbound connections are now properly closed when the peer closes.

- Bugfix: The DNS-resolver will trap recursive resolution attempts (may happen when the cluster runs in a docker-container on the client).

- Bugfix: The TUN-device will trap failed connection attempts that results in recursive calls back into the TUN-device (may happen when the
  cluster runs in a docker-container on the client).

- Bugfix: Fixed a potential deadlock when a new agent joined the traffic manager.

- Bugfix: The app-version value of the Helm chart embedded in the telepresence binary is now automatically updated at build time. The value is hardcoded in the
  original Helm chart when we release so this fix will only affect our nightly builds.

- Bugfix: The configured webhookRegistry is now propagated to the webhook installer even if no webhookAgentImage has been set.

- Bugfix: Login logs the user in when their access token has expired, instead of having no effect.

### 2.4.6 (November 2, 2021)

- Feature: Telepresence CLI is now built and published for Apple silicon Macs.

- Feature: Telepresence now supports manually injecting the traffic-agent YAML into workload manifests.
  Use the `genyaml` command to create the sidecar YAML, then add the `telepresence.getambassador.io/manually-injected: "true"` annotation to to your pods to allow Telepresence to intercept them.

- Feature: Added a json flag for the "telepresence list" command. This will aid automation.

- Change: `--help` text now includes a link to https://www.telepresence.io/ so users who download Telepresence via Brew or some other mechanism are able to find the documentation easily.

- Bugfix: Telepresence will no longer attempt to proxy requests to the API server when it happens to have an IP address within the CIDR range of pods/services.

### 2.4.5 (October 15, 2021)

- Feature: Intercepting headless services is now supported. It's now possible to request a headless service on whatever port it exposes and get a response from the intercept.

- Feature: Preview url questions have more context and provide "best guess" defaults.

- Feature: The `gather-logs` command added two new flags. One for anonymizing pod names + namespaces and the other for getting the pod yaml of the `traffic-manager` and any pod that contains a `traffic-agent`.

- Change: Use one tunnel per connection instead of multiplexing into one tunnel. This client will still be backwards compatible with older `traffic-manager`s that only support multiplexing.

- Bugfix: Telepresence will now log that the kubernetes server version is unsupported when using a version older than 1.17.

- Bugfix: Telepresence only adds the security context when necessary: intercepting a headless service or using a numeric port with the webhook agent injector.

### 2.4.4 (September 27, 2021)

- Feature: The strategy used by traffic-manager's discovery of pod CIDRs can now be configured using the Helm chart.

- Feature: Add the command `telepresence gather-logs`, which bundles the logs for all components
  into one zip file that can then be shared in a github issue, in slack, etc.  Use
  `telepresence gather-logs --help` to see additional options for running the command.

- Feature: The agent injector now supports injecting Traffic Agents into pods that have unnamed ports.

- Bugfix: The traffic-manager now uses less CPU-cycles when computing the pod CIDRs.

- Bugfix: If a deployment annotated with webhook annotations is deployed before telepresence is installed, telepresence will now install an agent in that deployment before intercept

- Bugfix: Fix an issue where the traffic-manager would sometimes go into a CPU loop.

- Bugfix: The TUN-device no longer builds an unlimited internal buffer before sending it when receiving lots of TCP-packets without PSH.
  Instead, the buffer is flushed when it reaches a size of 64K.

- Bugfix: The user daemon would sometimes hang when it encountered a problem connecting to the cluster or the root daemon.

- Bugfix: Telepresence correctly reports an intercept port conflict instead of panicking with segfault.

### 2.4.3 (September 15, 2021)

- Feature: The environment variable `TELEPRESENCE_INTERCEPT_ID` is now available in the interceptor's environment.

- Bugfix: A timing related bug was fixed that sometimes caused a "daemon did not start" failure.

- Bugfix: On Windows, crash stack traces and other errors were not
  written to the log files, now they are.

- Bugfix: On Linux kernel 4.11 and above, the log file rotation now
  properly reads the birth-time of the log file.  On older kernels, it
  continues to use the old behavior of using the change-time in place
  of the birth-time.

- Bugfix: Telepresence will no longer refer the user to the daemon logs for errors that aren't related to
  problems that are logged there.

- Bugfix: The overriding DNS resolver will no longer apply search paths when resolving "localhost".

- Bugfix: The cluster domain used by the DNS resolver is retrieved from the traffic-manager instead of being
  hard-coded to "cluster.local".

- Bugfix: "Telepresence uninstall --everything" now also uninstalls agents installed via mutating webhook

- Bugfix: Downloading large files during an intercept will no longer cause timeouts and hanging traffic-agent.

- Bugfix: Passing false to the intercept command's --mount flag will no longer result in a filesystem being mounted.

- Bugfix: The traffic manager will establish outbound connections in parallel instead of sequentially.

- Bugfix: The `telepresence status` command reports correct DNS settings instead of "Local IP: nil, Remote IP: nil"

### 2.4.2 (September 1, 2021)

- Feature: A new `telepresence loglevel <level>` subcommand was added that enables changing the loglevel
  temporarily for the local daemons, the `traffic-manager` and the `traffic-agents`.

- Change: The default log-level is now `info` for all components of Telepresence.

- Bugfix: The overriding DNS resolver will no longer apply search paths when resolving "localhost".

- Bugfix: The RBAC was not updated in the helm chart to enable the traffic-manager to `get` and `list`
  namespaces, which would impact users who use licensed features of the Telepresence extensions in an
  air-gapped environment.

- Bugfix: The timeout for Helm actions wasn't always respected which could cause a failing install of the
  `traffic-manager` to make the user daemon to hang indefinitely.

### 2.4.1 (August 30, 2021)

- Bugfix: Telepresence will now mount all directories from `/var/run/secrets`, not just the kubernetes.io ones.
  This allows the mounting of secrets directories such as eks.amazonaws.com (for IRSA tokens)

- Bugfix: The grpc.maxReceiveSize setting is now correctly propagated to all grpc servers.
  This allows users to mitigate a root daemon crash when sending a message over the default maximum size.

- Bugfix: Some slight fixes to the `homebrew-package.sh` script which will enable us to run
  it manually if we ever need to make homebrew point at an older version.

- Feature: Helm chart has now feature to on demand regenerate certificate used for mutating webhook by setting value.
  `agentInjector.certificate.regenerate`

- Change: The traffic-manager now requires `get` namespace permissions to get the cluster ID instead of that value being
  passed in as an environment variable to the traffic-manager's deployment.

- Change: The traffic-manager is now installed via an embedded version of the Helm chart when `telepresence connect` is first performed on a cluster.
  This change is transparent to the user.
  A new configuration flag, `timeouts.helm` sets the timeouts for all helm operations performed by the Telepresence binary.

- Bugfix: Telepresence will initialize the default namespace from the kubeconfig on each call instead of just doing it when connecting.

- Bugfix: The timeout to keep idle outbound TCP connections alive was increased from 60 to 7200 seconds which is the same as
  the Linux `tcp_keepalive_time` default.

- Bugfix: Telepresence will now remove a socket that is the result of an ungraceful termination and retry instead of printing
  an error saying "this usually means that the process has terminated ungracefully"

- Change: Failure to report metrics is logged using loglevel info rather than error.

- Bugfix: A potential deadlock situation is fixed that sometimes caused the user daemon to hang when the user
  was logged in.

- Feature: The scout reports will now include additional metadata coming from environment variables starting with
  `TELEPRESENCE_REPORT_`.

- Bugfix: The config setting `images.agentImage` is no longer required to contain the repository. The repository is
  instead picked from `images.repository`.

- Change: The `registry`, `webhookRegistry`, `agentImage` and `webhookAgentImage` settings in the `images` group of the `config.yml`
  now get their defaults from `TELEPRESENCE_AGENT_IMAGE` and `TELEPRESENCE_REGISTRY`.

### 2.4.0 (August 4, 2021)

- Feature: There is now a native Windows client for Telepresence.
  All the same features supported by the MacOS and Linux client are available on Windows.

- Feature: Telepresence can now receive messages from the cloud and raise
  them to the user when they perform certain commands.

- Bugfix: Initialization of `systemd-resolved` based DNS sets
  routing domain to improve stability in non-standard configurations.

- Bugfix: Edge case error when targeting a container by port number.
  Before if your matching/target container was at containers list index 0,
  but if there was a container at index 1 with no ports, then the
  "no ports" container would end up the selected one

- Bugfix: A `$(NAME)` reference in the agent's environment will now be
  interpolated correctly.

- Bugfix: Telepresence will no longer print an INFO level log message when
  no config.yml file is found.

- Bugfix: A panic is no longer raised when passing an argument to the
  `telepresence intercept` option `--http-match` that does't contain an
  equal sign.

- Bugfix: The `traffic-manager` will only send subnet updates to a
  client root daemon when the subnets actually change.

- Bugfix: The agent uninstaller now distinguishes between recoverable
  and unrecoverable failures, allowing uninstallation from manually changed
  resources

### 2.3.7 (July 23, 2021)

- Feature: An `also-proxy` entry in the Kubernetes cluster config will
  show up in the output of the `telepresence status` command.

- Feature: `telepresence login` now has an `--apikey=KEY` flag that
  allows for non-interactive logins.  This is useful for headless
  environments where launching a web-browser is impossible, such as
  cloud shells, Docker containers, or CI.

- Bugfix: Dialer will now close if it gets a ConnectReject. This was
  encountered when doing an intercept without a local process running
  and would result in requests hanging indefinitely.

- Bugfix: Made `telepresence list` command faster.

- Bugfix: Mutating webhook injector correctly hides named ports for probes.

- Bugfix: Initialization of `systemd-resolved` based DNS is more stable and
  failures causing telepresence to default to the overriding resolver will no
  longer cause general DNS lookup failures.

- Bugfix: Fixed a regression introduced in 2.3.5 that caused `telepresence current-cluster-id`
  to crash.

- Bugfix: New API keys generated internally for communication with
  Ambassador Cloud no longer show up as "no description" in the
  Ambassador Cloud web UI.  Existing API keys generated by older
  versions of Telepresence will still show up this way.

- Bugfix: Fixed a race condition that logging in and logging out
  rapidly could cause memory corruption or corruption of the
  `user-info.json` cache file used when authenticating with Ambassador
  Cloud.

### 2.3.6 (July 20, 2021)

- Bugfix: Fixed a regression introduced in 2.3.5 that caused preview
  URLs to not work.

- Bugfix: Fixed a regression introduced in 2.3.5 where the Traffic
  Manager's `RoleBinding` did not correctly appoint the
  `traffic-manager` `Role`, causing subnet discovery to not be able to
  work correctly.

- Bugfix: Fixed a regression introduced in 2.3.5 where the root daemon
  did not correctly read the configuration file; ignoring the user's
  configured log levels and timeouts.

- Bugfix: Fixed an issue that could cause the user daemon to crash
  during shutdown, as during shutdown it unconditionally attempted to
  close a channel even though the channel might already be closed.

### 2.3.5 (July 15, 2021)

- Feature: Telepresence no longer depends on having an external
  `kubectl` binary, which might not be present for OpenShift users
  (who have `oc` instead of `kubectl`).
- Feature: `skipLogin` can be used in the config.yml to tell the cli not to connect to cloud when using an air-gapped environment.
- Feature: The Telepresence Helm chart now supports installing multiple
  Traffic Managers in multiple namespaces. This will allow operators to
  install Traffic Managers with limited permissions that match the
  permissions restrictions that Telepresence users are subject to.
- Feature: The maximum size of messages that the client can receive over gRPC can now be configured. The gRPC default of 4MB isn't enough
  under some circumstances.
- Change: `TELEPRESENCE_AGENT_IMAGE` and `TELEPRESENCE_REGISTRY` are now only configurable via config.yml.
- Bugfix: Fixed and improved several error messages, to hopefully be
  more helpful.
- Bugfix: Fixed a DNS problem on macOS causing slow DNS lookups when connecting to a local cluster.

### 2.3.4 (July 9, 2021)

- Bugfix: Some log statements that contained garbage instead of a proper IP address now produce the correct address.
- Bugfix: Telepresence will no longer panic when multiple services match a workload.
- Bugfix: The traffic-manager will now accurately determine the service subnet by creating a dummy-service in its own namespace.
- Bugfix: Telepresence connect will no longer try to update the traffic-manager's clusterrole if the live one is identical to the desired one.
- Bugfix: The Telepresence helm chart no longer fails when installing with `--set clientRbac.namespaced=true`

### 2.3.3 (July 7, 2021)

- Feature: Telepresence now supports installing the Traffic Manager
  via Helm.  This will make it easy for operators to install and
  configure the server-side components of Telepresence separately from
  the CLI (which in turn allows for better separation of permissions).

- Feature: As the `traffic-manager` can now be installed in any
  namespace via Helm, Telepresence can now be configured to look for
  the traffic manager in a namespace other than `ambassador`.  This
  can be configured on a per-cluster basis.

- Feature: `telepresence intercept` now supports a `--to-pod` flag
  that can be used to port-forward sidecars' ports from an intercepted
  pod

- Feature: `telepresence status` now includes more information about
  the root daemon.

- Feature: We now do nightly builds of Telepresence for commits on release/v2 that haven't been tagged and published yet.

- Change: Telepresence no longer automatically shuts down the old
  `api_version=1` `edgectl` daemon.  If migrating from such an old
  version of `edgectl` you must now manually shut down the `edgectl`
  daemon before running Telepresence.  This was already the case when
  migrating from the newer `api_version=2` `edgectl`.

- Bugfix: The root daemon no longer terminates when the user daemon
  disconnects from its gRPC streams, and instead waits to be
  terminated by the CLI.  This could cause problems with things not
  being cleaned up correctly.

- Bugfix: An intercept will survive deletion of the intercepted pod
  provided that another pod is created (or already exists) that can
  take over.

### 2.3.2 (June 18, 2021)

- Feature: The mutator webhook for injecting traffic-agents now
  recognizes a `telepresence.getambassador.io/inject-service-port`
  annotation to specify which port to intercept; bringing the
  functionality of the `--port` flag to users who use the mutator
  webook in order to control Telepresence via GitOps.

- Feature: Outbound connections are now routed through the intercepted
  Pods which means that the connections originate from that Pod from
  the cluster's perspective.  This allows service meshes to correctly
  identify the traffic.

- Change: Inbound connections from an intercepted agent are now
  tunneled to the manager over the existing gRPC connection, instead
  of establishing a new connection to the manager for each inbound
  connection.  This avoids interference from certain service mesh
  configurations.

- Change: The traffic-manager requires RBAC permissions to list Nodes,
  Pods, and to create a dummy Service in the manager's namespace.

- Change: The on-laptop client no longer requires RBAC permissions to
  list Nodes in the cluster or to create Services, as that
  functionality has been moved to the traffic-manager.

- Bugfix: Telepresence will now detect the pod CIDR ranges even if
  they are not listed in the Nodes.

- Bugfix: The list of cluster subnets that the virtual network
  interface will route is now configured dynamically and will follow
  changes in the cluster.

- Bugfix: Subnets fully covered by other subnets are now pruned
  internally and thus never superfluously added to the laptop's
  routing table.

- Change: The `trafficManagerAPI` timout default has changed from 5
  seconds to 15 seconds, in order to facilitate the extended time it
  takes for the traffic-manager to do its initial discovery of cluster
  info as a result of the above bugfixes.

- Bugfix: On macOS, files generated under `/etc/resolver/` as the
  result of using `include-suffixes` in the cluster config are now
  properly removed on quit.

- Bugfix: Telepresence no longer erroneously terminates connections
  early when sending a large HTTP response from an intercepted
  service.

- Bugfix: When shutting down the user-daemon or root-daemon on the
  laptop, `telepresence quit` and related commands no longer return
  early before everything is fully shut down.  Now it can be counted
  on that by the time the command has returned that all of the
  side-effects on the laptop have been cleaned up.

### 2.3.1 (June 14, 2021)

- Feature: Agents can now be installed using a mutator webhook
- Feature: DNS resolver can now be configured with respect to what IP addresses that are used, and what lookups that gets sent to the cluster.
- Feature: Telepresence can now be configured to proxy subnets that aren't part of the cluster but only accesible from the cluster.
- Change: The `trafficManagerConnect` timout default has changed from 20 seconds to 60 seconds, in order to facilitate
  the extended time it takes to apply everything needed for the mutator webhook.
- Change: Telepresence is now installable via `brew install datawire/blackbird/telepresence`
- Bugfix: Fix a bug where sometimes large transfers from services on the cluster would hang indefinitely

### 2.3.0 (June 1, 2021)

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
- Bugfix: The root-user daemon now respects the timeouts in the normal user's configuration file.

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
- Bugfix: IPv6 routes now work and don't prevent other pfctl rules being written in macOS
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
  + macOS: `~/Library/Logs/telepresence/`
  + Linux: `$XDG_CACHE_HOME/telepresence/logs/` or `$HOME/.cache/telepresence/logs/`
