---
title: telepresence mcp vscode disable
description: Remove server from VSCode config
hide_table_of_contents: true
---

Remove server from VSCode config

## Synopsis:

Remove this application from VSCode MCP servers

### Usage:
```
  telepresence mcp vscode disable [flags]
```

### Flags:
```
      --config-path string   Path to VSCode config file
      --config-type string   Configuration type: 'workspace' or 'user' (default: user)
  -h, --help                 help for disable
      --server-name string   Name of the MCP server to remove (default: derived from executable name)
      --workspace            Remove from workspace settings (.vscode/mcp.json) instead of user settings
```

### Global Flags:
```
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default "default")
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default "auto")
      --use string        Match expression that uniquely identifies the daemon container
```
