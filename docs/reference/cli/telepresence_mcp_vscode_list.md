---
title: telepresence mcp vscode list
description: Show VSCode MCP servers
hide_table_of_contents: true
---

Show VSCode MCP servers

## Synopsis:

Show all MCP servers configured in VSCode

### Usage:
```
  telepresence mcp vscode list [flags]
```

### Flags:
```
      --config-path string   Path to VSCode config file
      --config-type string   Configuration type: 'workspace' or 'user' (default: user)
  -h, --help                 help for list
      --workspace            List from workspace settings (.vscode/mcp.json) instead of user settings
```

### Global Flags:
```
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default "default")
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default "auto")
      --use string        Match expression that uniquely identifies the daemon container
```
