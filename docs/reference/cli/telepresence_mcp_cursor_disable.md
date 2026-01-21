---
title: telepresence mcp cursor disable
description: Remove server from Cursor config
hide_table_of_contents: true
---

Remove server from Cursor config

## Synopsis:

Remove this application from Cursor MCP servers

### Usage:
```
  telepresence mcp cursor disable [flags]
```

### Flags:
```
      --config-path string   Path to Cursor config file
  -h, --help                 help for disable
      --server-name string   Name of the MCP server to remove (default: derived from executable name)
      --workspace            Remove from workspace settings (.cursor/mcp.json) instead of user settings
```

### Global Flags:
```
      --config string     Path to the Telepresence configuration file (default &quot;$HOME/.config/telepresence/config.yml&quot;)
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default &quot;default&quot;)
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default &quot;auto&quot;)
      --use string        Match expression that uniquely identifies the daemon container
```
