---
title: telepresence compose export
description: Export a service container's filesystem as a tar archive
hide_table_of_contents: true
---

Export a service container's filesystem as a tar archive

### Usage:
```
  telepresence compose export [flags] [services]
```

### Flags:
```
  -h, --help   help for export
```

### Compose flags:
```
      --env-file stringArray       Optional environment files
  -f, --file stringArray           Compose configuration files
      --profile stringArray        Profile to enable
      --project-directory string   Specify an alternate working directory (default: the path of the, first specified, Compose file)
      --project-name string        Project name
```

### Compose export flags:
```
      --index int       index of the container if service has multiple
  -o, --output string   Write to a file, instead of STDOUT
```

### Global Flags:
```
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default "default")
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default "auto")
      --use string        Match expression that uniquely identifies the daemon container
```
