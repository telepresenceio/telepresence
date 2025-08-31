---
title: telepresence genyaml config
description: Generate YAML for the agent's entry in the telepresence-agents configmap.
hide_table_of_contents: true
---

Generate YAML for the agent's entry in the telepresence-agents configmap.

## Synopsis:

Generate YAML for the agent's entry in the telepresence-agents configmap. See genyaml for more info on what this means

### Usage:
```
  telepresence genyaml config [flags]
```

### Flags:
```
      --agent-image string         The qualified name of the agent image (default "ghcr.io/telepresenceio/tel2:2.24.1")
      --agent-port uint16          The port number you wish the agent to listen on. (default 9900)
  -h, --help                       help for config
  -i, --input string               Path to the yaml containing the workload definition (i.e. Deployment, StatefulSet, etc). Pass '-' for stdin.. Mutually exclusive to --workload
      --loglevel string            The loglevel for the generated traffic-agent sidecar (default "info")
      --manager-namespace string   The traffic-manager namespace (default "ambassador")
      --manager-port uint16        The traffic-manager API port (default 8081)
  -n, --namespace string           If present, the namespace scope for this CLI request
  -o, --output string              Path to the file to place the output in. Defaults to '-' which means stdout. (default "-")
  -w, --workload string            Name of the workload. If given, the workload will be retrieved from the cluster, mutually exclusive to --input
```

### Kubernetes flags:
```
      --as string                      Username to impersonate for the operation. User could be a regular user or a service account in a namespace.
      --as-group stringArray           Group to impersonate for the operation, this flag can be repeated to specify multiple groups.
      --as-uid string                  UID to impersonate for the operation.
      --cache-dir string               Default cache directory (default "$HOME/.kube/cache")
      --certificate-authority string   Path to a cert file for the certificate authority
      --client-certificate string      Path to a client certificate file for TLS
      --client-key string              Path to a client key file for TLS
      --cluster string                 The name of the kubeconfig cluster to use
      --context string                 The name of the kubeconfig context to use
      --disable-compression            If true, opt-out of response compression for all requests to the server
      --insecure-skip-tls-verify       If true, the server's certificate will not be checked for validity. This will make your HTTPS connections insecure
      --kubeconfig string              Path to the kubeconfig file to use for CLI requests.
      --request-timeout string         The length of time to wait before giving up on a single server request. Non-zero values should contain a corresponding time unit (e.g. 1s, 2m, 3h). A value of zero means don't timeout requests. (default "0")
  -s, --server string                  The address and port of the Kubernetes API server
      --tls-server-name string         Server name to use for server certificate validation. If it is not provided, the hostname used to contact the server is used
      --token string                   Bearer token for authentication to the API server
      --user string                    The name of the kubeconfig user to use
```

### Global Flags:
```
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default "default")
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default "auto")
      --use string        Match expression that uniquely identifies the daemon container
```
