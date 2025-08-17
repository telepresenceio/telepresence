---
title: telepresence genyaml annotations
description: Generate YAML for the pod template metadata annotations.
hide_table_of_contents: true
---

Generate YAML for the pod template metadata annotations.

## Synopsis:

Generate YAML for the pod template metadata annotations. See genyaml for more info on what this means

### Usage:
```
  telepresence genyaml annotations [flags]
```

### Flags:
```
  -c, --config string   Path to the yaml containing the generated configmap entry
  -h, --help            help for annotations
  -o, --output string   Path to the file to place the output in. Defaults to '-' which means stdout. (default "-")
```

### Global Flags:
```
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default "default")
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default "auto")
      --use string        Match expression that uniquely identifies the daemon container
```
