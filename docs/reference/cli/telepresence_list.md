---
title: telepresence list
description: List current intercepts
hide_table_of_contents: true
---

List current intercepts

### Usage:
```
  telepresence list [flags]
```

### Flags:
```
  -a, --agents             with installed agents only
      --debug              include debugging information
  -h, --help               help for list
  -g, --ingests            ingests
  -i, --intercepts         intercepts
  -n, --namespace string   If present, the namespace scope for this CLI request
  -r, --replacements       replacements
  -t, --wiretaps           wiretaps
```

### Global Flags:
```
      --config string     Path to the Telepresence configuration file (default &quot;$HOME/.config/telepresence/config.yml&quot;)
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default &quot;default&quot;)
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default &quot;auto&quot;)
      --use string        Match expression that uniquely identifies the daemon container
```
