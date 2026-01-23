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
      --remove-orphans   Remove containers for services not defined in the Compose file
      --rmi string       Remove images used by services. &quot;local&quot; remove only images that don't have a custom tag (&quot;local&quot;|&quot;all&quot;)
  -t, --timeout int      Specify a shutdown timeout in seconds
  -v, --volumes          Remove named volumes declared in the &quot;volumes&quot; section of the Compose file and anonymous volumes attached to containers
```

### Global Flags:
```
      --config string     Path to the Telepresence configuration file (default &quot;$HOME/.config/telepresence/config.yml&quot;)
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default &quot;default&quot;)
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default &quot;auto&quot;)
      --use string        Match expression that uniquely identifies the daemon container
```
