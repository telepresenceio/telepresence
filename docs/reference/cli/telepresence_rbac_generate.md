---
title: telepresence rbac generate
description: Generate RBAC manifests for an admin to review and apply
hide_table_of_contents: true
---

Generate RBAC manifests for an admin to review and apply

### Usage:
```
  telepresence rbac generate [command] [flags]
```

### Available Commands:
| Command | Description |
|---------|-------------|
| [client](telepresence_rbac_generate_client) | Generate the RBAC that a Telepresence client needs |
| [install](telepresence_rbac_generate_install) | Generate the RBAC that telepresence helm install needs |

### Flags:
```
  -h, --help   help for generate
```

### Global Flags:
```
      --config string     Path to the Telepresence configuration file
      --format string     Set the output format, supported values are 'json', 'yaml', 'json-stream', and 'default' (default &quot;default&quot;)
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default &quot;auto&quot;)
      --use string        Match expression that uniquely identifies the daemon container
```

Use `telepresence rbac generate [command] --help` for more information about a command.
