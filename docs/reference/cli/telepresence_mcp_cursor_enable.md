---
title: telepresence mcp cursor enable
description: Add server to Cursor config
hide_table_of_contents: true
---

Add server to Cursor config

## Synopsis:

Add this application as an MCP server in Cursor

### Usage:
```
  telepresence mcp cursor enable [flags]
```

### Flags:
```
      --config-path string   Path to Cursor config file
  -e, --env stringToString   Environment variables (e.g., --env KEY1=value1 --env KEY2=value2) (default [])
  -h, --help                 help for enable
      --log-level string     Log level (debug, info, warn, error)
      --server-name string   Name for the MCP server (default: derived from executable name)
      --workspace            Add to workspace settings (.cursor/mcp.json) instead of user settings
```

### Global Flags:
```
      --config string     Path to the Telepresence configuration file (default &quot;$HOME/.config/telepresence/config.yml&quot;)
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default &quot;default&quot;)
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default &quot;auto&quot;)
      --use string        Match expression that uniquely identifies the daemon container
```
