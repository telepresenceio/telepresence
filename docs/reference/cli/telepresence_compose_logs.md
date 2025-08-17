---
title: telepresence compose logs
description: View output from containers
hide_table_of_contents: true
---

View output from containers

### Usage:
```
  telepresence compose logs [flags] [services]
```

### Flags:
```
  -h, --help   help for logs
```

### Compose flags:
```
      --env-file stringArray       Optional environment files
  -f, --file stringArray           Compose configuration files
      --profile stringArray        Profile to enable
      --project-directory string   Specify an alternate working directory (default: the path of the, first specified, Compose file)
      --project-name string        Project name
```

### Compose logs flags:
```
  -f, --follow          Follow log output
      --index int       index of the container if service has multiple
      --no-color        Produce monochrome output
      --no-log-prefix   Don't print prefix in logs
      --since string    Show logs since timestamp (e.g.
  -n, --tail string     Number of lines to show from the end of the logs
  -t, --timestamps      Show timestamps
      --until string    Show logs before a timestamp (e.g.
```

### Global Flags:
```
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default "default")
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default "auto")
      --use string        Match expression that uniquely identifies the daemon container
```
