---
title: telepresence rbac verify install
description: Check that an identity may run telepresence helm install
hide_table_of_contents: true
---

Check that an identity may run telepresence helm install

## Synopsis:

Check that an identity may run telepresence helm install.

The command renders the embedded chart for the given values, checks the same
permissions as telepresence rbac generate install for the identity of the
kubeconfig (after --as and --as-group), and prints what's missing. It exits
with a non-zero status when anything is missing.

The traffic-manager namespace is --manager-namespace, or ambassador when it's
not given. These commands have no -n, so if you install with
telepresence helm install -n foo, pass --manager-namespace foo here. It isn't
looked up in the cluster.

### Usage:
```
  telepresence rbac verify install [flags]
```

### Flags:
```
  -h, --help                       help for install
      --input string               Read a Helm values file that overlays the chart defaults
      --manager-namespace string   The namespace where the traffic manager is to be found. Overrides any other manager namespace set in config
      --verbs string               What to check: &quot;install&quot; for a first install, &quot;lifecycle&quot; to also check upgrades and uninstalls (default &quot;install&quot;)
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
