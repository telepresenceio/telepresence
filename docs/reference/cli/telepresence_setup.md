---
title: telepresence setup
description: Analyze the cluster and propose or apply a traffic-manager configuration
hide_table_of_contents: true
---

Analyze the cluster and propose or apply a traffic-manager configuration

## Synopsis:

Analyze the cluster and propose or apply a traffic-manager configuration.

The command probes the cluster (privileges, QUIC viability, node-agent
viability, webhook creation, namespace scale, and any existing installation),
asks a small number of questions that the findings make relevant, and prints a
report with a generated Helm values document. Without --output or --apply the
command only validates the setup; --output writes the values file, and --apply
installs or upgrades the traffic-manager with it.

### Usage:
```
  telepresence setup [flags]
```

### Flags:
```
      --allow-conflicting-subnets strings   Comma separated list of CIDR that will be allowed to conflict with local subnets
      --also-proxy strings                  Additional comma separated list of CIDR to proxy
      --apply                               Install/upgrade the traffic-manager with the resulting values
      --attach                              Clients will attach to workloads (intercept/replace/ingest/wiretap) (default true)
      --docker                              Start, or connect to, daemon in a docker container
      --expose stringArray                  Port that a containerized daemon will expose. See docker run -p for more info. Can be repeated
  -h, --help                                help for setup
      --hostname string                     Hostname used by a containerized daemon
      --input string                        Read a Helm values file; its settings become pinned defaults
      --managed-namespaces strings          Namespace list when --scope=namespaces or --scope=mapped
      --manager-namespace string            The namespace where the traffic manager is to be found. Overrides any other manager namespace set in config
      --mapped-namespaces strings           Comma separated list of namespaces considered by DNS resolver and NAT for outbound connections. Defaults to all namespaces
      --name string                         Optional name to use for the connection
  -n, --namespace string                    If present, the namespace scope for this CLI request
      --never-proxy strings                 Comma separated list of CIDR to never proxy
      --node-agent string                   Override the node-agent probe verdict (auto|on|off) (default &quot;auto&quot;)
      --non-interactive                     Never prompt; unanswered questions fall back to flag values, input-pinned settings, or safe defaults
      --output string                       Write the resulting Helm values to this file, suitable for a Helm install; &quot;-&quot; writes them to stdout and suppresses the report
      --proxy-via strings                   Use Network Address Translation to create virtual IPs for the given CIDR, and route via WORKLOAD. Must be in the form CIDR=WORKLOAD. CIDR can be substituted for the symblic name &quot;service&quot;, &quot;pods&quot;, &quot;also&quot;, or &quot;all&quot;.
      --quic string                         Override the QUIC probe verdict (auto|on|off) (default &quot;auto&quot;)
      --replace                             The replace command will be used
      --reroute-local strings               Reroute port on local host to remote host. Format is &lt;local port&gt;:&lt;host&gt;:&lt;port&gt;[/{tcp,udp}]. &lt;port&gt; can be symbolic when &lt;host&gt; is a service name.
      --reroute-remote strings              Reroute port on remote host. Format is &lt;host&gt;:&lt;port&gt;:&lt;new port&gt;[/{tcp,udp}]. &lt;port&gt; can be symbolic when &lt;host&gt; is a service name.
      --scope string                        Namespace limiting strategy (all|namespaces|selector|mapped)
      --upgrade-manager                     Upgrade an existing, older traffic-manager (default true)
      --vnat strings                        Use Network Address Translation to create virtual IPs for the given CIDR. CIDR can be substituted for the symblic name &quot;service&quot;, &quot;pods&quot;, &quot;also&quot;, or &quot;all&quot;.
```

### Kubernetes flags:
```
      --as string                      Username to impersonate for the operation. User could be a regular user or a service account in a namespace.
      --as-group stringArray           Group to impersonate for the operation, this flag can be repeated to specify multiple groups.
      --as-uid string                  UID to impersonate for the operation.
      --as-user-extra stringArray      User extras to impersonate for the operation, this flag can be repeated to specify multiple values for the same key.
      --cache-dir string               Default cache directory (default &quot;$HOME/.kube/cache&quot;)
      --certificate-authority string   Path to a cert file for the certificate authority
      --client-certificate string      Path to a client certificate file for TLS
      --client-key string              Path to a client key file for TLS
      --cluster string                 The name of the kubeconfig cluster to use
      --context string                 The name of the kubeconfig context to use
      --disable-compression            If true, opt-out of response compression for all requests to the server
      --insecure-skip-tls-verify       If true, the server's certificate will not be checked for validity. This will make your HTTPS connections insecure
      --kubeconfig string              Path to the kubeconfig file to use for CLI requests.
      --request-timeout string         The length of time to wait before giving up on a single server request. Non-zero values should contain a corresponding time unit (e.g. 1s, 2m, 3h). A value of zero means don't timeout requests. (default &quot;0&quot;)
  -s, --server string                  The address and port of the Kubernetes API server
      --tls-server-name string         Server name to use for server certificate validation. If it is not provided, the hostname used to contact the server is used
      --token string                   Bearer token for authentication to the API server
      --user string                    The name of the kubeconfig user to use
```

### Global Flags:
```
      --config string     Path to the Telepresence configuration file
      --format string     Set the output format, supported values are 'json', 'yaml', 'json-stream', and 'default' (default &quot;default&quot;)
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default &quot;auto&quot;)
      --use string        Match expression that uniquely identifies the daemon container
```
