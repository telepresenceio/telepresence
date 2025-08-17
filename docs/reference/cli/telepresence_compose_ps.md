---
title: telepresence compose ps
description: List containers
hide_table_of_contents: true
---

List containers

### Usage:
```
  telepresence compose ps [flags] [services]
```

### Flags:
```
  -h, --help   help for ps
```

### Compose flags:
```
      --env-file stringArray       Optional environment files
  -f, --file stringArray           Compose configuration files
      --profile stringArray        Profile to enable
      --project-directory string   Specify an alternate working directory (default: the path of the, first specified, Compose file)
      --project-name string        Project name
```

### Compose ps flags:
```
  -a, --all                  Show all stopped containers (including those
      --filter string        Filter services by a property (supported
      --format string        Format output using a custom template:
      --no-trunc             Don't truncate output
      --orphans              Include orphaned services (not declared by
  -q, --quiet                Only display IDs
      --services             Display services
      --status stringArray   Filter services by status. Values: [paused |
```

### Global Flags:
```
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default "default")
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default "auto")
      --use string        Match expression that uniquely identifies the daemon container
```
