---
title: telepresence compose attach
description: Attach local standard input, output, and error streams to a service's running container
hide_table_of_contents: true
---

Attach local standard input, output, and error streams to a service's running container

### Usage:
```
  telepresence compose attach [flags] [services]
```

### Flags:
```
  -h, --help   help for attach
```

### Compose flags:
```
      --env-file stringArray       Optional environment files
  -f, --file stringArray           Compose configuration files
      --profile stringArray        Profile to enable
      --project-directory string   Specify an alternate working directory (default: the path of the, first specified, Compose file)
      --project-name string        Project name
```

### Compose attach flags:
```
      --detach-keys string   Override the key sequence for detaching from a container.
      --index int            index of the container if service has multiple replicas.
      --no-stdin             Do not attach STDIN
      --sig-proxy            Proxy all received signals to the process (default true)
```

### Global Flags:
```
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default "default")
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default "auto")
      --use string        Match expression that uniquely identifies the daemon container
```
