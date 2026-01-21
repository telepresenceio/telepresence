---
title: telepresence compose
description: Define and run multi-container applications with Telepresence and Docker
hide_table_of_contents: true
---

Define and run multi-container applications with Telepresence and Docker

### Usage:
```
  telepresence compose [command] [flags]
```

### Available Commands:
| Command | Description |
|---------|-------------|
| [attach](telepresence_compose_attach) | Attach local standard input, output, and error streams to a service's running container |
| [bridge](telepresence_compose_bridge) | Convert compose files into another model |
| [build](telepresence_compose_build) | Build or rebuild services |
| [commit](telepresence_compose_commit) | Create a new image from a service container's changes |
| [config](telepresence_compose_config) | Parse, resolve and render compose file in canonical format |
| [cp](telepresence_compose_cp) | docker compose cp [OPTIONS] SRC_PATH|- SERVICE:DEST_PATH |
| [create](telepresence_compose_create) | Creates containers for a service |
| [down](telepresence_compose_down) | Stop and remove containers, networks |
| [events](telepresence_compose_events) | Receive real time events from containers |
| [exec](telepresence_compose_exec) | Execute a command in a running container |
| [export](telepresence_compose_export) | Export a service container's filesystem as a tar archive |
| [images](telepresence_compose_images) | List images used by the created containers |
| [kill](telepresence_compose_kill) | Force stop service containers |
| [logs](telepresence_compose_logs) | View output from containers |
| [ls](telepresence_compose_ls) | List running compose projects |
| [pause](telepresence_compose_pause) | Pause services |
| [port](telepresence_compose_port) | Print the public port for a port binding |
| [ps](telepresence_compose_ps) | List containers |
| [publish](telepresence_compose_publish) | Publish compose application |
| [pull](telepresence_compose_pull) | Pull service images |
| [push](telepresence_compose_push) | Push service images |
| [restart](telepresence_compose_restart) | Restart service containers |
| [rm](telepresence_compose_rm) | Removes stopped service containers |
| [run](telepresence_compose_run) | Run a one-off command on a service |
| [scale](telepresence_compose_scale) | Scale services |
| [start](telepresence_compose_start) | Start services |
| [stats](telepresence_compose_stats) | Display a live stream of container(s) resource usage statistics |
| [stop](telepresence_compose_stop) | Stop services |
| [top](telepresence_compose_top) | Display the running processes |
| [unpause](telepresence_compose_unpause) | Unpause services |
| [up](telepresence_compose_up) | Create and start containers |
| [version](telepresence_compose_version) | Show the Docker Compose version information |
| [volumes](telepresence_compose_volumes) | List volumes |
| [wait](telepresence_compose_wait) | Block until containers of all (or specified) services stop. |
| [watch](telepresence_compose_watch) | Watch build context for service and rebuild/refresh containers when files are updated |

### Flags:
```
  -h, --help   help for compose
```

### Compose flags:
```
      --env-file stringArray       Optional environment files
  -f, --file stringArray           Compose configuration files
      --profile stringArray        Profile to enable
      --project-directory string   Specify an alternate working directory (default: the path of the, first specified, Compose file)
      --project-name string        Project name
```

### Global Flags:
```
      --config string     Path to the Telepresence configuration file (default &quot;$HOME/.config/telepresence/config.yml&quot;)
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default &quot;default&quot;)
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default &quot;auto&quot;)
      --use string        Match expression that uniquely identifies the daemon container
```

Use `telepresence compose [command] --help` for more information about a command.
