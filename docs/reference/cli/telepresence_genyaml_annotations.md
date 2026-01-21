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
  -a, --agent string    Path to the yaml containing the generated agent config
  -h, --help            help for annotations
  -o, --output string   Path to the file to place the output in. Defaults to '-' which means stdout. (default &quot;-&quot;)
```

### Global Flags:
```
      --config string     Path to the Telepresence configuration file (default &quot;$HOME/.config/telepresence/config.yml&quot;)
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default &quot;default&quot;)
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default &quot;auto&quot;)
      --use string        Match expression that uniquely identifies the daemon container
```
