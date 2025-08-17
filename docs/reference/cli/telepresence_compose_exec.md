---
title: telepresence compose exec
description: Execute a command in a running container
hide_table_of_contents: true
---

Execute a command in a running container

### Usage:
```
  telepresence compose exec [flags] [services]
```

### Flags:
```
  -h, --help   help for exec
```

### Compose flags:
```
      --env-file stringArray       Optional environment files
  -f, --file stringArray           Compose configuration files
      --profile stringArray        Profile to enable
      --project-directory string   Specify an alternate working directory (default: the path of the, first specified, Compose file)
      --project-name string        Project name
```

### Compose exec flags:
```
  -d, --detach            Detached mode: Run command in the
  -e, --env stringArray   Set environment variables
      --index int         Index of the container if service
      --privileged        Give extended privileges to the process
  -u, --user string       Run the command as this user
  -w, --workdir string    Path to workdir directory for this
```

### Global Flags:
```
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default "default")
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default "auto")
      --use string        Match expression that uniquely identifies the daemon container
```
