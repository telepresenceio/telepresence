---
title: telepresence connect
description: Connect to a cluster
hide_table_of_contents: true
---

Connect to a cluster

### Usage:
```
  telepresence connect [flags] [-- &lt;command to run while connected&gt;]
```

### Flags:
```
      --allow-conflicting-subnets strings   Comma separated list of CIDR that will be allowed to conflict with local subnets
      --also-proxy strings                  Additional comma separated list of CIDR to proxy
      --docker                              Start, or connect to, daemon in a docker container
      --expose stringArray                  Port that a containerized daemon will expose. See docker run -p for more info. Can be repeated
  -h, --help                                help for connect
      --hostname string                     Hostname used by a containerized daemon
      --manager-namespace string            The namespace where the traffic manager is to be found. Overrides any other manager namespace set in config
      --mapped-namespaces strings           Comma separated list of namespaces considered by DNS resolver and NAT for outbound connections. Defaults to all namespaces
      --name string                         Optional name to use for the connection
  -n, --namespace string                    If present, the namespace scope for this CLI request
      --never-proxy strings                 Comma separated list of CIDR to never proxy
      --proxy-via strings                   Use Network Address Translation to create virtual IPs for the given CIDR, and route via WORKLOAD. Must be in the form CIDR=WORKLOAD. CIDR can be substituted for the symblic name &quot;service&quot;, &quot;pods&quot;, &quot;also&quot;, or &quot;all&quot;.
      --reroute-local strings               Reroute port on local host to remote host. Format is &lt;local port&gt;:&lt;host&gt;:&lt;port&gt;[/{tcp,udp}]. &lt;port&gt; can be symbolic when &lt;host&gt; is a service name.
      --reroute-remote strings              Reroute port on remote host. Format is &lt;host&gt;:&lt;port&gt;:&lt;new port&gt;[/{tcp,udp}]. &lt;port&gt; can be symbolic when &lt;host&gt; is a service name.
      --vnat strings                        Use Network Address Translation to create virtual IPs for the given CIDR. CIDR can be substituted for the symblic name &quot;service&quot;, &quot;pods&quot;, &quot;also&quot;, or &quot;all&quot;.
```

### Kubernetes flags:
```
      --as string                      Username to impersonate for the operation. User could be a regular user or a service account in a namespace.
      --as-group stringArray           Group to impersonate for the operation, this flag can be repeated to specify multiple groups.
      --as-uid string                  UID to impersonate for the operation.
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
      --config string     Path to the Telepresence configuration file (default &quot;$HOME/.config/telepresence/config.yml&quot;)
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default &quot;default&quot;)
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default &quot;auto&quot;)
      --use string        Match expression that uniquely identifies the daemon container
```
