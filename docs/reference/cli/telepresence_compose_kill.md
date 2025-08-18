---
title: telepresence compose kill
description: Force stop service containers
hide_table_of_contents: true
---

Force stop service containers

### Usage:
```
  telepresence compose kill [flags] [services]
```

### Flags:
```
  -h, --help   help for kill
```

### Compose flags:
```
      --env-file stringArray       Optional environment files
  -f, --file stringArray           Compose configuration files
      --profile stringArray        Profile to enable
      --project-directory string   Specify an alternate working directory (default: the path of the, first specified, Compose file)
      --project-name string        Project name
```

### Compose kill flags:
```
      --remove-orphans   Remove containers for services not defined in
  -s, --signal string    SIGNAL to send to the container (default "\"SIGKILL\"")
```

### Global Flags:
```
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default "default")
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default "auto")
      --use string        Match expression that uniquely identifies the daemon container
```
