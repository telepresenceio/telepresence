---
title: telepresence compose up
description: Create and start containers
hide_table_of_contents: true
---

Create and start containers

### Usage:
```
  telepresence compose up [flags] [services]
```

### Flags:
```
  -h, --help   help for up
```

### Compose flags:
```
      --env-file stringArray       Optional environment files
  -f, --file stringArray           Compose configuration files
      --profile stringArray        Profile to enable
      --project-directory string   Specify an alternate working directory (default: the path of the, first specified, Compose file)
      --project-name string        Project name
```

### Compose up flags:
```
      --abort-on-container-exit      Stops all containers if any
      --abort-on-container-failure   Stops all containers if any
      --always-recreate-deps         Recreate dependent containers.
      --attach stringArray           Restrict attaching to the specified
      --attach-dependencies          Automatically attach to log output
      --build                        Build images before starting containers
  -d, --detach                       Detached mode: Run containers in the
      --exit-code-from string        Return the exit code of the selected
      --force-recreate               Recreate containers even if their
      --menu                         Enable interactive shortcuts when
      --no-attach stringArray        Do not attach (stream logs) to the
      --no-build                     Don't build an image, even if it's policy
      --no-color                     Produce monochrome output
      --no-deps                      Don't start linked services
      --no-log-prefix                Don't print prefix in logs
      --no-recreate                  If containers already exist, don't
      --no-start                     Don't start the services after
      --pull string                  Pull image before running
      --quiet-build                  Suppress the build output
      --quiet-pull                   Pull without printing progress
      --remove-orphans               Remove containers for services not
  -V, --renew-anon-volumes           Recreate anonymous volumes instead
      --scale uint32                 Scale SERVICE to NUM instances.
  -t, --timeout int                  Use this timeout in seconds for
      --timestamps                   Show timestamps
      --wait                         Wait for services to be
      --wait-timeout int             Maximum duration in seconds to wait
  -w, --watch                        Watch source code and
  -y, --yes                          Assume "yes" as answer to all
```

### Global Flags:
```
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default "default")
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default "auto")
      --use string        Match expression that uniquely identifies the daemon container
```
