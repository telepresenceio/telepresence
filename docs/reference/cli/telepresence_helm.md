---
title: telepresence helm
description: Helm commands using the embedded Telepresence Helm chart.
hide_table_of_contents: true
---

Helm commands using the embedded Telepresence Helm chart.

### Usage:
```
  telepresence helm [command] [flags]
```

### Available Commands:
| Command | Description |
|---------|-------------|
| [install](telepresence_helm_install) | Install telepresence traffic manager |
| [lint](telepresence_helm_lint) | Verify the embedded telepresence Helm chart |
| [uninstall](telepresence_helm_uninstall) | Uninstall telepresence traffic manager |
| [upgrade](telepresence_helm_upgrade) | Upgrade telepresence traffic manager |
| [version](telepresence_helm_version) | Print the version of the Helm client |

### Flags:
```
  -h, --help   help for helm
```

### Global Flags:
```
      --config string     Path to the Telepresence configuration file (default &quot;$HOME/.config/telepresence/config.yml&quot;)
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default &quot;default&quot;)
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default &quot;auto&quot;)
      --use string        Match expression that uniquely identifies the daemon container
```

Use `telepresence helm [command] --help` for more information about a command.
