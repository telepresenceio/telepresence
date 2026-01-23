---
title: telepresence compose create
description: Creates containers for a service
hide_table_of_contents: true
---

Creates containers for a service

### Usage:
```
  telepresence compose create [flags] [services]
```

### Flags:
```
  -h, --help   help for create
```

### Compose flags:
```
      --env-file stringArray       Optional environment files
  -f, --file stringArray           Compose configuration files
      --profile stringArray        Profile to enable
      --project-directory string   Specify an alternate working directory (default: the path of the, first specified, Compose file)
      --project-name string        Project name
```

### Compose create flags:
```
      --build            Build images before starting containers
      --force-recreate   Recreate containers even if their configuration and image haven't changed
      --no-build         Don't build an image, even if it's policy
      --no-recreate      If containers already exist, don't recreate them. Incompatible with --force-recreate.
      --pull string      Pull image before running (&quot;always&quot;|&quot;missing&quot;|&quot;never&quot;|&quot;build&quot;) (default &quot;policy&quot;)
      --quiet-pull       Pull without printing progress information
      --remove-orphans   Remove containers for services not defined in the Compose file
      --scale uint32     Scale SERVICE to NUM instances. Overrides the scale setting in the Compose file if present.
  -y, --yes              Assume &quot;yes&quot; as answer to all prompts and run non-interactively
```

### Global Flags:
```
      --config string     Path to the Telepresence configuration file (default &quot;$HOME/.config/telepresence/config.yml&quot;)
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default &quot;default&quot;)
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default &quot;auto&quot;)
      --use string        Match expression that uniquely identifies the daemon container
```
