---
title: telepresence compose version
description: Show the Docker Compose version information
hide_table_of_contents: true
---

Show the Docker Compose version information

### Usage:
```
  telepresence compose version [flags] [services]
```

### Flags:
```
  -h, --help   help for version
```

### Compose flags:
```
      --env-file stringArray       Optional environment files
  -f, --file stringArray           Compose configuration files
      --profile stringArray        Profile to enable
      --project-directory string   Specify an alternate working directory (default: the path of the, first specified, Compose file)
      --project-name string        Project name
```

### Compose version flags:
```
  -f, --format string   Format the output. Values: [pretty | json]. (Default: pretty)
      --short           Shows only Compose's version number
```

### Global Flags:
```
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default "default")
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default "auto")
      --use string        Match expression that uniquely identifies the daemon container
```
