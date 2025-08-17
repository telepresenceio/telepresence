---
title: telepresence compose stats
description: Display a live stream of container(s) resource usage statistics
hide_table_of_contents: true
---

Display a live stream of container(s) resource usage statistics

### Usage:
```
  telepresence compose stats [flags] [services]
```

### Flags:
```
  -h, --help   help for stats
```

### Compose flags:
```
      --env-file stringArray       Optional environment files
  -f, --file stringArray           Compose configuration files
      --profile stringArray        Profile to enable
      --project-directory string   Specify an alternate working directory (default: the path of the, first specified, Compose file)
      --project-name string        Project name
```

### Compose stats flags:
```
  -a, --all             Show all containers (default shows just running)
      --format string   Format output using a custom template:
      --no-stream       Disable streaming stats and only pull the first result
      --no-trunc        Do not truncate output
```

### Global Flags:
```
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default "default")
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default "auto")
      --use string        Match expression that uniquely identifies the daemon container
```
