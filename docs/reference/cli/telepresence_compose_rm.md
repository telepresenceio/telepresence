---
title: telepresence compose rm
description: Removes stopped service containers
hide_table_of_contents: true
---

Removes stopped service containers

### Usage:
```
  telepresence compose rm [flags] [services]
```

### Flags:
```
  -h, --help   help for rm
```

### Compose flags:
```
      --env-file stringArray       Optional environment files
  -f, --file stringArray           Compose configuration files
      --profile stringArray        Profile to enable
      --project-directory string   Specify an alternate working directory (default: the path of the, first specified, Compose file)
      --project-name string        Project name
```

### Compose rm flags:
```
  -f, --force     Don't ask to confirm removal
  -s, --stop      Stop the containers, if required, before removing
  -v, --volumes   Remove any anonymous volumes attached to containers
```

### Global Flags:
```
      --config string     Path to the Telepresence configuration file (default &quot;$HOME/.config/telepresence/config.yml&quot;)
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default &quot;default&quot;)
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default &quot;auto&quot;)
      --use string        Match expression that uniquely identifies the daemon container
```
