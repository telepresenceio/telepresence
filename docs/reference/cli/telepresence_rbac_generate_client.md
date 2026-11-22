---
title: telepresence rbac generate client
description: Generate the RBAC that a Telepresence client needs
hide_table_of_contents: true
---

Generate the RBAC that a Telepresence client needs

## Synopsis:

Generate the RBAC that a Telepresence client needs.

The command renders the chart's client roles for the configuration the
traffic-manager is installed with, and prints them for the given subjects.
Nothing is applied.

The configuration is read from the traffic-manager release with your own
credentials, also when --as is given, which needs read access to the Helm
release secrets in the manager namespace. Without that access, or for an install that doesn't
exist yet, pass the values with --input.

--subject names the subject of the bindings, as User:NAME, Group:NAME or
ServiceAccount:NAMESPACE/NAME. --namespace limits the rules to the given
namespaces, which the traffic-manager must manage. --emit values prints a
clientRbac values block for the release instead of manifests.

### Usage:
```
  telepresence rbac generate client [flags]
```

### Flags:
```
      --emit string                What to print: &quot;rbac&quot; for manifests, &quot;values&quot; for a clientRbac values block (default &quot;rbac&quot;)
  -h, --help                       help for client
      --input string               Read the values the traffic-manager is installed with from a Helm values file
      --manager-namespace string   The namespace where the traffic manager is to be found. Overrides any other manager namespace set in config
      --namespace stringArray      Namespace the client may use (repeatable, default every managed namespace)
      --subject stringArray        Subject of the bindings, as User:NAME, Group:NAME or ServiceAccount:NAMESPACE/NAME (repeatable)
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
      --proxy-url string               Proxy URL to use for requests to the API server
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
