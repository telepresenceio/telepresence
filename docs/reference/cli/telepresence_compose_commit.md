---
title: telepresence compose commit
description: Create a new image from a service container's changes
hide_table_of_contents: true
---

Create a new image from a service container's changes

### Usage:
```
  telepresence compose commit [flags] [services]
```

### Flags:
```
  -h, --help   help for commit
```

### Compose flags:
```
      --env-file stringArray       Optional environment files
  -f, --file stringArray           Compose configuration files
      --profile stringArray        Profile to enable
      --project-directory string   Specify an alternate working directory (default: the path of the, first specified, Compose file)
      --project-name string        Project name
```

### Compose commit flags:
```
  -a, --author string    Author (e.g., &quot;John Hannibal Smith &lt;hannibal@a-team.com&gt;&quot;)
  -c, --change list      Apply Dockerfile instruction to the created image
      --index int        index of the container if service has multiple replicas.
  -m, --message string   Commit message
  -p, --pause            Pause container during commit (default true)
```

### Global Flags:
```
      --config string     Path to the Telepresence configuration file (default &quot;$HOME/.config/telepresence/config.yml&quot;)
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default &quot;default&quot;)
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default &quot;auto&quot;)
      --use string        Match expression that uniquely identifies the daemon container
```
