---
title: telepresence compose images
description: List images used by the created containers
hide_table_of_contents: true
---

List images used by the created containers

### Usage:
```
  telepresence compose images [flags] [services]
```

### Flags:
```
  -h, --help   help for images
```

### Compose flags:
```
      --env-file stringArray       Optional environment files
  -f, --file stringArray           Compose configuration files
      --profile stringArray        Profile to enable
      --project-directory string   Specify an alternate working directory (default: the path of the, first specified, Compose file)
      --project-name string        Project name
```

### Compose images flags:
```
      --format string   Format the output. Values: [table | json]
  -q, --quiet           Only display IDs
```

### Global Flags:
```
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default "default")
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default "auto")
      --use string        Match expression that uniquely identifies the daemon container
```
