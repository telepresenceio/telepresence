---
title: telepresence compose down
description: Stop and remove containers, networks
hide_table_of_contents: true
---

Stop and remove containers, networks

### Usage:
```
  telepresence compose down [flags] [services]
```

### Flags:
```
  -h, --help   help for down
```

### Compose flags:
```
      --env-file stringArray       Optional environment files
  -f, --file stringArray           Compose configuration files
      --profile stringArray        Profile to enable
      --project-directory string   Specify an alternate working directory (default: the path of the, first specified, Compose file)
      --project-name string        Project name
```

### Compose down flags:
```
      --remove-orphans   Remove containers for services not defined in
      --rmi string       Remove images used by services. "local" remove
  -t, --timeout int      Specify a shutdown timeout in seconds
  -v, --volumes          Remove named volumes declared in the "volumes"
```

### Global Flags:
```
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default "default")
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default "auto")
      --use string        Match expression that uniquely identifies the daemon container
```
