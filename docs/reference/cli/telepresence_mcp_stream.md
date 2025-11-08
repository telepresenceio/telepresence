---
title: telepresence mcp stream
description: Stream the MCP server over HTTP
hide_table_of_contents: true
---

Stream the MCP server over HTTP

## Synopsis:

Start HTTP server to expose CLI commands to AI assistants

### Usage:
```
  telepresence mcp stream [flags]
```

### Flags:
```
  -h, --help               help for stream
      --host string        host to listen on
      --log-level string   Log level (debug, info, warn, error)
      --port int           port number to listen on (default 8080)
```

### Global Flags:
```
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default "default")
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default "auto")
      --use string        Match expression that uniquely identifies the daemon container
```
