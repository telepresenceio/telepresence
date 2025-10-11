---
title: telepresence mcp
description: MCP server management
hide_table_of_contents: true
---

MCP server management

## Synopsis:

Manage MCP servers for AI assistants and code editors

### Usage:
```
  telepresence mcp [command] [flags]
```

### Available Commands:
| Command | Description |
|---------|-------------|
| [claude](telepresence_mcp_claude) | Configure Claude Desktop MCP servers |
| [start](telepresence_mcp_start) | Start the MCP server |
| [tools](telepresence_mcp_tools) | Export tools as JSON |
| [vscode](telepresence_mcp_vscode) | Configure VSCode MCP servers |

### Flags:
```
  -h, --help   help for mcp
```

### Global Flags:
```
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default "default")
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default "auto")
      --use string        Match expression that uniquely identifies the daemon container
```

Use `telepresence mcp [command] --help` for more information about a command.
