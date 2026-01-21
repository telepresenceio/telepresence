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
      --abort-on-container-exit      Stops all containers if any container was stopped. Incompatible with -d
      --abort-on-container-failure   Stops all containers if any container exited with failure. Incompatible with -d
      --always-recreate-deps         Recreate dependent containers. Incompatible with --no-recreate.
      --attach stringArray           Restrict attaching to the specified services. Incompatible with --attach-dependencies.
      --attach-dependencies          Automatically attach to log output of dependent services
      --build                        Build images before starting containers
  -d, --detach                       Detached mode: Run containers in the background
      --exit-code-from string        Return the exit code of the selected service container. Implies --abort-on-container-exit
      --force-recreate               Recreate containers even if their configuration and image haven't changed
      --menu                         Enable interactive shortcuts when running attached. Incompatible with --detach. Can also be enable/disable by setting COMPOSE_MENU environment var.
      --no-attach stringArray        Do not attach (stream logs) to the specified services
      --no-build                     Don't build an image, even if it's policy
      --no-color                     Produce monochrome output
      --no-deps                      Don't start linked services
      --no-log-prefix                Don't print prefix in logs
      --no-recreate                  If containers already exist, don't recreate them. Incompatible with --force-recreate.
      --no-start                     Don't start the services after creating them
      --pull string                  Pull image before running (&quot;always&quot;|&quot;missing&quot;|&quot;never&quot;) (default &quot;policy&quot;)
      --quiet-build                  Suppress the build output
      --quiet-pull                   Pull without printing progress information
      --remove-orphans               Remove containers for services not defined in the Compose file
  -V, --renew-anon-volumes           Recreate anonymous volumes instead of retrieving data from the previous containers
      --scale uint32                 Scale SERVICE to NUM instances. Overrides the scale setting in the Compose file if present.
  -t, --timeout int                  Use this timeout in seconds for container shutdown when attached or when containers are already running
      --timestamps                   Show timestamps
      --wait                         Wait for services to be running|healthy. Implies detached mode.
      --wait-timeout int             Maximum duration in seconds to wait for the project to be running|healthy
  -w, --watch                        Watch source code and rebuild/refresh containers when files are updated.
  -y, --yes                          Assume &quot;yes&quot; as answer to all prompts and run non-interactively
```

### Global Flags:
```
      --config string     Path to the Telepresence configuration file (default &quot;$HOME/.config/telepresence/config.yml&quot;)
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default &quot;default&quot;)
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default &quot;auto&quot;)
      --use string        Match expression that uniquely identifies the daemon container
```
