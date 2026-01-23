---
title: telepresence compose wait
description: Block until containers of all (or specified) services stop.
hide_table_of_contents: true
---

Block until containers of all (or specified) services stop.

### Usage:
```
  telepresence compose wait [flags] [services]
```

### Flags:
```
  -h, --help   help for wait
```

### Compose flags:
```
      --env-file stringArray       Optional environment files
  -f, --file stringArray           Compose configuration files
      --profile stringArray        Profile to enable
      --project-directory string   Specify an alternate working directory (default: the path of the, first specified, Compose file)
      --project-name string        Project name
```

### Compose wait flags:
```
      --down-project   Drops project when the first container stops
```

### Global Flags:
```
      --config string     Path to the Telepresence configuration file (default &quot;$HOME/.config/telepresence/config.yml&quot;)
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default &quot;default&quot;)
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default &quot;auto&quot;)
      --use string        Match expression that uniquely identifies the daemon container
```
