---
title: telepresence compose watch
description: Watch build context for service and rebuild/refresh containers when files are updated
hide_table_of_contents: true
---

Watch build context for service and rebuild/refresh containers when files are updated

### Usage:
```
  telepresence compose watch [flags] [services]
```

### Flags:
```
  -h, --help   help for watch
```

### Compose flags:
```
      --env-file stringArray       Optional environment files
  -f, --file stringArray           Compose configuration files
      --profile stringArray        Profile to enable
      --project-directory string   Specify an alternate working directory (default: the path of the, first specified, Compose file)
      --project-name string        Project name
```

### Compose watch flags:
```
      --no-up   Do not build & start services before watching
      --prune   Prune dangling images on rebuild (default true)
      --quiet   hide build output
```

### Global Flags:
```
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default "default")
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default "auto")
      --use string        Match expression that uniquely identifies the daemon container
```
