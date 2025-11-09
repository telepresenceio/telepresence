---
title: telepresence mcp cursor
description: Manage Cursor MCP servers
hide_table_of_contents: true
---

Manage Cursor MCP servers

## Synopsis:

Manage MCP server configuration for Cursor

### Usage:
```
  telepresence mcp cursor [command] [flags]
```

### Available Commands:
| Command | Description |
|---------|-------------|
| [disable](telepresence_mcp_cursor_disable) | Remove server from Cursor config |
| [enable](telepresence_mcp_cursor_enable) | Add server to Cursor config |
| [list](telepresence_mcp_cursor_list) | Show Cursor MCP servers |

### Flags:
```
  -h, --help   help for cursor
```

### Global Flags:
```
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default "default")
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default "auto")
      --use string        Match expression that uniquely identifies the daemon container
```

Use `telepresence mcp cursor [command] --help` for more information about a command.
