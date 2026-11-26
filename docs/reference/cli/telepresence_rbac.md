---
title: telepresence rbac
description: Generate or check the RBAC that installing or using Telepresence needs
hide_table_of_contents: true
---

Generate or check the RBAC that installing or using Telepresence needs

### Usage:
```
  telepresence rbac [command] [flags]
```

### Available Commands:
| Command | Description |
|---------|-------------|
| [generate](telepresence_rbac_generate) | Generate RBAC manifests for an admin to review and apply |
| [verify](telepresence_rbac_verify) | Check that an identity holds the RBAC that installing or using Telepresence needs |

### Flags:
```
  -h, --help   help for rbac
```

### Global Flags:
```
      --config string     Path to the Telepresence configuration file
      --format string     Set the output format, supported values are 'json', 'yaml', 'json-stream', and 'default' (default &quot;default&quot;)
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default &quot;auto&quot;)
      --use string        Match expression that uniquely identifies the daemon container
```

Use `telepresence rbac [command] --help` for more information about a command.
