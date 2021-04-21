---
Description: "How telepresence intercept can run a Docker container with configured environment and volume mounts."
---

# Using Docker for intercepts

The Telepresence intercept command can automatically run a Docker container which has been configured with the environment and volume mounts of the intercepted service. This is done using the option `--docker-run`.

`telepresence intercept [service] --port [port] --docker-run -- [arguments]`

This command will intercept a service and start `docker run` in the foreground. The intercept ends when the run ends.

Telepresence will automatically pass some relevant flags to Docker in order to connect the container with the intercept. Those flags are combined with the arguments given after `--` on the command line (the `--` separates flags intended for `telepresence intercept` from flags intended for `docker run`).

The `--port` flag can specify an additional port when `--docker-run` is used so that the local and container port can be different. This is done using `--port [local port]:[container port]`. The container port will default to the local port when using the `--port [port]` syntax

The flags that telepresence will pass (invisibly) to `docker run` are:

- `--dns-search tel2-search` Enables single label name lookups in intercepted namespaces.
- `--env-file [file]` Loads the intercepted environment.
- `--name intercept-{intercept name}-{intercept port}` Names the Docker container. This flag is omitted if explicitly given on the command line.
- `-p [port:container-port]` The local port for the intercept and the container port.
- `-v [local mount dir:docker mount dir]` Volume mount specification. See CLI help for `--mount` and `--docker-mount` flags for more info.
