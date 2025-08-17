---
title: telepresence genyaml
description: Generate YAML for use in kubernetes manifests.
hide_table_of_contents: true
---

Generate YAML for use in kubernetes manifests.

## Synopsis:

Generate traffic-agent yaml for use in kubernetes manifests.
This allows the traffic agent to be injected by hand into existing kubernetes manifests.
For your modified workload to be valid, you'll have to manually inject annotations, a
container, and a volume into the workload; you can do this by running "genyaml config",
"genyaml container", "genyaml initcontainer", "genyaml annotations", and "genyaml volume".

NOTE: It is recommended that you not do this unless strictly necessary. Instead, we suggest letting
telepresence's webhook injector configure the traffic agents on demand.

### Usage:
```
  telepresence genyaml [command] [flags]
```

### Available Commands:
| Command | Description |
|---------|-------------|
| [annotations](telepresence_genyaml_annotations) | Generate YAML for the pod template metadata annotations. |
| [config](telepresence_genyaml_config) | Generate YAML for the agent's entry in the telepresence-agents configmap. |
| [container](telepresence_genyaml_container) | Generate YAML for the traffic-agent container. |
| [initcontainer](telepresence_genyaml_initcontainer) | Generate YAML for the traffic-agent init container. |
| [volume](telepresence_genyaml_volume) | Generate YAML for the traffic-agent volume. |

### Flags:
```
  -h, --help            help for genyaml
  -o, --output string   Path to the file to place the output in. Defaults to '-' which means stdout. (default "-")
```

### Global Flags:
```
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default "default")
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default "auto")
      --use string        Match expression that uniquely identifies the daemon container
```

Use `telepresence genyaml [command] --help` for more information about a command.
