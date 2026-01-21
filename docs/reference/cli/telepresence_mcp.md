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
| [claude](telepresence_mcp_claude) | Manage Claude Desktop MCP servers |
| [cursor](telepresence_mcp_cursor) | Manage Cursor MCP servers |
| [start](telepresence_mcp_start) | Start the MCP server |
| [stream](telepresence_mcp_stream) | Stream the MCP server over HTTP |
| [tools](telepresence_mcp_tools) | Export tools as JSON |
| [vscode](telepresence_mcp_vscode) | Manage VSCode MCP servers |

### Flags:
```
  -h, --help   help for mcp
```

### Global Flags:
```
      --config string     Path to the Telepresence configuration file (default &quot;$HOME/.config/telepresence/config.yml&quot;)
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default &quot;default&quot;)
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default &quot;auto&quot;)
      --use string        Match expression that uniquely identifies the daemon container
```

Use `telepresence mcp [command] --help` for more information about a command.
