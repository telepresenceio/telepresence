---
title: telepresence compose volumes
description: List volumes
hide_table_of_contents: true
---

List volumes

### Usage:
```
  telepresence compose volumes [flags] [services]
```

### Flags:
```
  -h, --help   help for volumes
```

### Compose flags:
```
      --env-file stringArray       Optional environment files
  -f, --file stringArray           Compose configuration files
      --profile stringArray        Profile to enable
      --project-directory string   Specify an alternate working directory (default: the path of the, first specified, Compose file)
      --project-name string        Project name
```

### Compose volumes flags:
```
      --format string   Format output using a custom template: 'table':            Print output in table format with column headers (default) 'table TEMPLATE':   Print output in table format using the given Go template 'json':             Print in JSON format 'TEMPLATE':         Print output using the given Go template. Refer to https://docs.docker.com/go/formatting/ for more information about formatting output with templates (default "table")
  -q, --quiet           Only display volume names
```

### Global Flags:
```
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default "default")
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default "auto")
      --use string        Match expression that uniquely identifies the daemon container
```
