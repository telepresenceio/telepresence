---
title: telepresence local-client-redirect
description: Manage local client redirects
hide_table_of_contents: true
---

Manage local client redirects

### Usage:
```
  telepresence local-client-redirect [command] [flags]
```

### Available Commands:
| Command | Description |
|---------|-------------|
| [add](telepresence_local-client-redirect_add) | Redirect client traffic for a remote host and port to localhost |
| [list](telepresence_local-client-redirect_list) | List local client redirects |
| [remove](telepresence_local-client-redirect_remove) | Remove a local client redirect |

### Flags:
```
  -h, --help   help for local-client-redirect
```

### Global Flags:
```
      --config string     Path to the Telepresence configuration file
      --format string     Set the output format, supported values are 'json', 'yaml', 'json-stream', and 'default' (default &quot;default&quot;)
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default &quot;auto&quot;)
      --use string        Match expression that uniquely identifies the daemon container
```

Use `telepresence local-client-redirect [command] --help` for more information about a command.
