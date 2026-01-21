---
title: telepresence compose events
description: Receive real time events from containers
hide_table_of_contents: true
---

Receive real time events from containers

### Usage:
```
  telepresence compose events [flags] [services]
```

### Flags:
```
  -h, --help   help for events
```

### Compose flags:
```
      --env-file stringArray       Optional environment files
  -f, --file stringArray           Compose configuration files
      --profile stringArray        Profile to enable
      --project-directory string   Specify an alternate working directory (default: the path of the, first specified, Compose file)
      --project-name string        Project name
```

### Compose events flags:
```
      --json           Output events as a stream of json objects
      --since string   Show all events created since timestamp
      --until string   Stream events until this timestamp
```

### Global Flags:
```
      --config string     Path to the Telepresence configuration file (default &quot;$HOME/.config/telepresence/config.yml&quot;)
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default &quot;default&quot;)
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default &quot;auto&quot;)
      --use string        Match expression that uniquely identifies the daemon container
```
