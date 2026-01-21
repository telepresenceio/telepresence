---
title: telepresence compose config
description: Parse, resolve and render compose file in canonical format
hide_table_of_contents: true
---

Parse, resolve and render compose file in canonical format

### Usage:
```
  telepresence compose config [flags] [services]
```

### Flags:
```
  -h, --help   help for config
```

### Compose flags:
```
      --env-file stringArray       Optional environment files
  -f, --file stringArray           Compose configuration files
      --profile stringArray        Profile to enable
      --project-directory string   Specify an alternate working directory (default: the path of the, first specified, Compose file)
      --project-name string        Project name
```

### Compose config flags:
```
      --environment             Print environment used for interpolation.
      --format string           Format the output. Values: [yaml | json]
      --hash string             Print the service config hash, one per line.
      --images                  Print the image names, one per line.
      --lock-image-digests      Produces an override file with image digests
      --models                  Print the model names, one per line.
      --networks                Print the network names, one per line.
      --no-consistency          Don't check model consistency - warning: may produce invalid Compose output
      --no-env-resolution       Don't resolve service env files
      --no-interpolate          Don't interpolate environment variables
      --no-normalize            Don't normalize compose model
      --no-path-resolution      Don't resolve file paths
  -o, --output string           Save to file (default to stdout)
      --profiles                Print the profile names, one per line.
  -q, --quiet                   Only validate the configuration, don't print anything
      --resolve-image-digests   Pin image tags to digests
      --services                Print the service names, one per line.
      --variables               Print model variables and default values.
      --volumes                 Print the volume names, one per line.
```

### Global Flags:
```
      --config string     Path to the Telepresence configuration file (default &quot;$HOME/.config/telepresence/config.yml&quot;)
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default &quot;default&quot;)
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default &quot;auto&quot;)
      --use string        Match expression that uniquely identifies the daemon container
```
