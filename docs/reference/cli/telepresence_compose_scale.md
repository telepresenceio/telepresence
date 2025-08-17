---
title: telepresence compose scale
description: Scale services
hide_table_of_contents: true
---

Scale services

### Usage:
```
  telepresence compose scale [flags] [services]
```

### Flags:
```
  -h, --help   help for scale
```

### Compose flags:
```
      --env-file stringArray       Optional environment files
  -f, --file stringArray           Compose configuration files
      --profile stringArray        Profile to enable
      --project-directory string   Specify an alternate working directory (default: the path of the, first specified, Compose file)
      --project-name string        Project name
```

### Compose scale flags:
```
      --no-deps   Don't start linked services
```

### Global Flags:
```
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default "default")
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default "auto")
      --use string        Match expression that uniquely identifies the daemon container
```
