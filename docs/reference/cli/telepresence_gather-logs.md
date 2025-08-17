---
title: telepresence gather-logs
description: Gather logs from traffic-manager, traffic-agent, user and root daemons, and export them into a zip file.
hide_table_of_contents: true
---

Gather logs from traffic-manager, traffic-agent, user and root daemons, and export them into a zip file.

## Synopsis:

Gather logs from traffic-manager, traffic-agent, user and root daemons,
and export them into a zip file. Useful if you are opening a Github issue or asking
someone to help you debug Telepresence.

### Usage:
```
  telepresence gather-logs [flags]
```

### Examples:
```
Here are a few examples of how you can use this command:
# Get all logs and export to a given file
telepresence gather-logs -o /tmp/telepresence_logs.zip

# Get all logs and pod yaml manifests for components in the kubernetes cluster
telepresence gather-logs -o /tmp/telepresence_logs.zip --get-pod-yaml

# Get all logs for the daemons only
telepresence gather-logs --traffic-agents=None --traffic-manager=False

# Get all logs for pods that have "echo-easy" in the name, useful if you have multiple replicas
telepresence gather-logs --traffic-manager=False --traffic-agents=echo-easy

# Get all logs for a specific pod
telepresence gather-logs --traffic-manager=False --traffic-agents=echo-easy-6848967857-tw4jw

# Get logs from everything except the daemons
telepresence gather-logs --daemons=None

```

### Flags:
```
  -a, --anonymize               To anonymize pod names + namespaces from the logs
      --daemons string          Comma separated list of daemons you want logs from: all, root, user, kubeauth, None (default "all")
  -y, --get-pod-yaml            Get the yaml of any pods you are getting logs for
  -h, --help                    help for gather-logs
  -o, --output-file string      The file you want to output the logs to.
      --traffic-agents string   Traffic-agents to collect logs from: all, name substring, None (default "all")
      --traffic-manager         If you want to collect logs from the traffic-manager (default true)
```

### Global Flags:
```
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default "default")
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default "auto")
      --use string        Match expression that uniquely identifies the daemon container
```
