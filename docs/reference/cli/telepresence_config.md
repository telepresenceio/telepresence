---
title: telepresence config
description: Telepresence configuration commands
hide_table_of_contents: true
---

Telepresence configuration commands

### Usage:
```
  telepresence config [command] [flags]
```

### Available Commands:
| Command | Description |
|---------|-------------|
| [view](telepresence_config_view) | View current Telepresence configuration |

### Flags:
```
  -h, --help   help for config
```

### Global Flags:
```
      --config string     Path to the Telepresence configuration file (default &quot;$HOME/.config/telepresence/config.yml&quot;)
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default &quot;default&quot;)
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default &quot;auto&quot;)
      --use string        Match expression that uniquely identifies the daemon container
```

Use `telepresence config [command] --help` for more information about a command.
