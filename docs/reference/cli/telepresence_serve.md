---
title: telepresence serve
description: Start the browser on a remote service
hide_table_of_contents: true
---

Start the browser on a remote service

### Usage:
```
  telepresence serve &lt;name of remote service&gt; [flags]
```

### Flags:
```
  -h, --help          help for serve
  -p, --port uint16   service port (default 80)
```

### Global Flags:
```
      --config string     Path to the Telepresence configuration file (default &quot;$HOME/.config/telepresence/config.yml&quot;)
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default &quot;default&quot;)
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default &quot;auto&quot;)
      --use string        Match expression that uniquely identifies the daemon container
```
