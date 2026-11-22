---
title: telepresence rbac verify
description: Check that an identity holds the RBAC that installing or using Telepresence needs
hide_table_of_contents: true
---

Check that an identity holds the RBAC that installing or using Telepresence needs

### Usage:
```
  telepresence rbac verify [command] [flags]
```

### Available Commands:
| Command | Description |
|---------|-------------|
| [client](telepresence_rbac_verify_client) | Check that an identity may use Telepresence |
| [install](telepresence_rbac_verify_install) | Check that an identity may run telepresence helm install |

### Flags:
```
  -h, --help   help for verify
```

### Global Flags:
```
      --config string     Path to the Telepresence configuration file
      --format string     Set the output format, supported values are 'json', 'yaml', 'json-stream', and 'default' (default &quot;default&quot;)
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default &quot;auto&quot;)
      --use string        Match expression that uniquely identifies the daemon container
```

Use `telepresence rbac verify [command] --help` for more information about a command.
