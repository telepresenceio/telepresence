---
title: telepresence compose push
description: Push service images
hide_table_of_contents: true
---

Push service images

### Usage:
```
  telepresence compose push [flags] [services]
```

### Flags:
```
  -h, --help   help for push
```

### Compose flags:
```
      --env-file stringArray       Optional environment files
  -f, --file stringArray           Compose configuration files
      --profile stringArray        Profile to enable
      --project-directory string   Specify an alternate working directory (default: the path of the, first specified, Compose file)
      --project-name string        Project name
```

### Compose push flags:
```
      --ignore-push-failures   Push what it can and ignores images with
      --include-deps           Also push images of services declared as
  -q, --quiet                  Push without printing progress information
```

### Global Flags:
```
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default "default")
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default "auto")
      --use string        Match expression that uniquely identifies the daemon container
```
