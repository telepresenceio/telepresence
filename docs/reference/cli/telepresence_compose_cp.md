---
title: telepresence compose cp
description: docker compose cp [OPTIONS] SRC_PATH|- SERVICE:DEST_PATH
hide_table_of_contents: true
---

docker compose cp [OPTIONS] SRC_PATH|- SERVICE:DEST_PATH

### Usage:
```
  telepresence compose cp [flags] [services]
```

### Flags:
```
  -h, --help   help for cp
```

### Compose flags:
```
      --env-file stringArray       Optional environment files
  -f, --file stringArray           Compose configuration files
      --profile stringArray        Profile to enable
      --project-directory string   Specify an alternate working directory (default: the path of the, first specified, Compose file)
      --project-name string        Project name
```

### Compose cp flags:
```
      --all           Include containers created by the run command
  -a, --archive       Archive mode (copy all uid/gid information)
  -L, --follow-link   Always follow symbol link in SRC_PATH
      --index int     Index of the container if service has multiple replicas
```

### Global Flags:
```
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default "default")
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default "auto")
      --use string        Match expression that uniquely identifies the daemon container
```
