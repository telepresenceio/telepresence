---
title: telepresence compose run
description: Run a one-off command on a service
hide_table_of_contents: true
---

Run a one-off command on a service

### Usage:
```
  telepresence compose run [flags] [services]
```

### Flags:
```
  -h, --help   help for run
```

### Compose flags:
```
      --env-file stringArray       Optional environment files
  -f, --file stringArray           Compose configuration files
      --profile stringArray        Profile to enable
      --project-directory string   Specify an alternate working directory (default: the path of the, first specified, Compose file)
      --project-name string        Project name
```

### Compose run flags:
```
      --build                       Build image before starting container
      --cap-add list                Add Linux capabilities
      --cap-drop list               Drop Linux capabilities
  -d, --detach                      Run container in background and print
      --entrypoint string           Override the entrypoint of the image
  -e, --env stringArray             Set environment variables
      --env-from-file stringArray   Set environment variables from file
  -i, --interactive                 Keep STDIN open even if not attached
  -l, --label stringArray           Add or override a label
      --name string                 Assign a name to the container
  -T, --no-TTY                      Disable pseudo-TTY allocation
      --no-deps                     Don't start linked services
  -p, --publish stringArray         Publish a container's port(s) to the host
      --pull string                 Pull image before running
  -q, --quiet                       Don't print anything to STDOUT
      --quiet-build                 Suppress progress output from the
      --quiet-pull                  Pull without printing progress information
      --remove-orphans              Remove containers for services not
      --rm                          Automatically remove the container
  -P, --service-ports               Run command with all service's ports
      --use-aliases                 Use the service's network useAliases
  -u, --user string                 Run as specified username or uid
  -v, --volume stringArray          Bind mount a volume
  -w, --workdir string              Working directory inside the container
```

### Global Flags:
```
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default "default")
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default "auto")
      --use string        Match expression that uniquely identifies the daemon container
```
