---
Description: "How a Telepresence intercept can run a Docker container with configured environment and volume mounts."
---

# Using Docker for intercepts

If you want your intercept to go to a Docker container on your laptop, use the `--docker-run` option. It creates the intercept, runs your container in the foreground, then automatically ends the intercept when the container exits.

`telepresence intercept <service_name> --port <port> --docker-run -- <arguments>`

The `--` separates flags intended for `telepresence intercept` from flags intended for `docker run`.

## Example

Imagine you are working on a new version of a your frontend service.  It is running in your cluster as a Deployment called `frontend-v1`. You use Docker on your laptop to build an improved version of the container called `frontend-v2`.  To test it out, use this command to run the new container on your laptop and start an intercept of the cluster service to your local container.

`telepresence intercept frontend-v1 --port 8000 --docker-run -- frontend-v2`

## Ports

The `--port` flag can specify an additional port when `--docker-run` is used so that the local and container port can be different. This is done using `--port <local port>:<container port>`. The container port will default to the local port when using the `--port <port>` syntax.

## Flags

Telepresence will automatically pass some relevant flags to Docker in order to connect the container with the intercept. Those flags are combined with the arguments given after `--` on the command line.

- `--dns-search tel2-search` Enables single label name lookups in intercepted namespaces
- `--env-file <file>` Loads the intercepted environment
- `--name intercept-<intercept name>-<intercept port>` Names the Docker container, this flag is omitted if explicitly given on the command line
- `-p <port:container-port>` The local port for the intercept and the container port
- `-v <local mount dir:docker mount dir>` Volume mount specification, see CLI help for `--mount` and `--docker-mount` flags for more info
