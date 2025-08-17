---
title: telepresence
description: Connect your workstation to a Kubernetes cluster
hide_table_of_contents: true
---

Connect your workstation to a Kubernetes cluster

## Synopsis:

Telepresence can connect to a cluster and route all outbound traffic from your
workstation to that cluster so that software running locally can communicate
as if it executed remotely, inside the cluster. This is achieved using the
command:
```bash
telepresence connect
```

Telepresence can also intercept traffic intended for a specific service in a
cluster and redirect it to your local workstation:

```bash
telepresence intercept <name of service>
```

Telepresence uses background processes to manage the cluster session. One of
the processes runs with superuser privileges because it modifies the network.
Unless the daemons are already started, an attempt will be made to start them.
This will involve a call to sudo unless this command is run as root (not
recommended) which in turn may result in a password prompt.

### Usage:
```
  telepresence [command] [flags]
```

### Available Commands:
| Command | Description |
|---------|-------------|
| [completion](telepresence_completion) | Generate a shell completion script |
| [compose](telepresence_compose) | Define and run multi-container applications with Telepresence and Docker |
| [config](telepresence_config) | Telepresence configuration commands |
| [connect](telepresence_connect) | Connect to a cluster |
| [curl](telepresence_curl) | curl with daemon network |
| [docker-run](telepresence_docker-run) | Docker run with daemon network |
| [gather-logs](telepresence_gather-logs) | Gather logs from traffic-manager, traffic-agent, user and root daemons, and export them into a zip file. |
| [genyaml](telepresence_genyaml) | Generate YAML for use in kubernetes manifests. |
| [helm](telepresence_helm) | Helm commands using the embedded Telepresence Helm chart. |
| [ingest](telepresence_ingest) | Ingest a container |
| [intercept](telepresence_intercept) | Intercept a service |
| [leave](telepresence_leave) | Remove existing intercept |
| [list](telepresence_list) | List current intercepts |
| [list-contexts](telepresence_list-contexts) | Show all contexts |
| [list-namespaces](telepresence_list-namespaces) | Show all namespaces |
| [loglevel](telepresence_loglevel) | Temporarily change the log-level of the traffic-manager, traffic-agent, and user and root daemons |
| [quit](telepresence_quit) | Tell telepresence daemons to quit |
| [replace](telepresence_replace) | Replace a container |
| [serve](telepresence_serve) | Start the browser on a remote service |
| [status](telepresence_status) | Show connectivity status |
| [uninstall](telepresence_uninstall) | Uninstall telepresence agents |
| [version](telepresence_version) | Show version |
| [wiretap](telepresence_wiretap) | Wiretap a Service |

### Flags:
```
  -h, --help   help for telepresence
```

### Global Flags:
```
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default "default")
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default "auto")
      --use string        Match expression that uniquely identifies the daemon container
```

Use `telepresence [command] --help` for more information about a command.
