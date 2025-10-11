---
title: telepresence mcp vscode
description: Configure VSCode MCP servers
hide_table_of_contents: true
---

Configure VSCode MCP servers

## Synopsis:

Configure MCP servers for Visual Studio Code

### Usage:
```
  telepresence mcp vscode [command] [flags]
```

### Available Commands:
| Command | Description |
|---------|-------------|
| [disable](telepresence_mcp_vscode_disable) | Remove server from VSCode config |
| [enable](telepresence_mcp_vscode_enable) | Add server to VSCode config |
| [list](telepresence_mcp_vscode_list) | Show VSCode MCP servers |

### Flags:
```
  -h, --help   help for vscode
```

### Global Flags:
```
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default "default")
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default "auto")
      --use string        Match expression that uniquely identifies the daemon container
```

Use `telepresence mcp vscode [command] --help` for more information about a command.
