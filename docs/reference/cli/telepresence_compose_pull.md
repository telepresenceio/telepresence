---
title: telepresence compose pull
description: Pull service images
hide_table_of_contents: true
---

Pull service images

### Usage:
```
  telepresence compose pull [flags] [services]
```

### Flags:
```
  -h, --help   help for pull
```

### Compose flags:
```
      --env-file stringArray       Optional environment files
  -f, --file stringArray           Compose configuration files
      --profile stringArray        Profile to enable
      --project-directory string   Specify an alternate working directory (default: the path of the, first specified, Compose file)
      --project-name string        Project name
```

### Compose pull flags:
```
      --ignore-buildable       Ignore images that can be built
      --ignore-pull-failures   Pull what it can and ignores images with pull failures
      --include-deps           Also pull services declared as dependencies
      --policy string          Apply pull policy ("missing"|"always")
  -q, --quiet                  Pull without printing progress information
```

### Global Flags:
```
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default "default")
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default "auto")
      --use string        Match expression that uniquely identifies the daemon container
```
