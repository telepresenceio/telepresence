# Developing Telepresence

## Development environment

### Building

The Open Source version of Telepresence consists of three artifacts.
- The `telepresence` binary. This is the binary that runs on the client.
  The same binary is used for the command line interface, the user daemon,
  and the root daemon.
- The `telepresence` docker image. Used by the container acting as both the
  user and root daemon when using `telepresence connect --docker`.
- The `tel2` docker image. Used by the traffic-manager and traffic-agent
  container.

- `TELEPRESENCE_REGISTRY` (required) is the Docker registry that
  `make push-images` pushes the `tel2` and `telepresence` image to.
  For most developers, the easiest thing is to set it to
  `docker.io/USERNAME`.

- `TELEPRESENCE_VERSION` (optional) is the "vSEMVER" string to
  compile-in to the telepresence binary and the telepresence and tel2
  Docker images, if set.  Otherwise, `make` will automatically set
  this based on the version found in the CHANGELOG.yml and a hash
  computed for the source.

The output of `make help` has a bit more information.

### Run Telepresence in a local cluster

Using the Kubernetes bundled with Docker Desktop is a quick and easy way to
develop Telepresence, because, when using this setup, there's no need to push
images to a registry. Kubernetes will find them in Docker's local cache
after they have been built.

Example building everything, install traffic-manager (with log-level debug),
and connect to it.
```bash
export TELEPRESENCE_VERSION=v2.22.0-alpha.0
export DEV_CLIENT_REGISTRY=local
make build client-image tel2-image
alias tel=./build-output/bin/telepresence
tel helm install --set logLevel=debug,image.pullPolicy=Never,agent.image.pullPolicy=Never
tel connect
```

### Environment Variables for Integration Testing

| Environment Name           | Description                                   | Default                   |
|----------------------------|-----------------------------------------------|---------------------------|
| `DEV_KUBECONFIG`           | Cluster configuration used by the tests       | Kubernetes default        |
| `DEV_CLIENT_REGISTRY`      | Docker registry for the client image          | "ghcr.io/telepresenceio"  |
| `DEV_MANAGER_REGISTRY`     | Docker registry for the traffic-manager image | Client registry           |
| `DEV_AGENT_REGISTRY`       | Docker registry for the traffic-agent image   | Traffic-manager registry  |
| `DEV_CLIENT_IMAGE`         | Name of the client image                      | "telepresence"            |
| `DEV_MANAGER_IMAGE`        | Name of the traffic-manager image             | "tel2"                    |
| `DEV_AGENT_IMAGE`          | Name of the traffic-agent image               | Traffic-manager image     |
| `DEV_CLIENT_VERSION`       | Client version                                | ${TELEPRESENCE_VERSION#v} |
| `DEV_MANAGER_VERSION`      | Traffic-manager version                       | Client version            |
| `DEV_AGENT_VERSION`        | Traffic-agent image version                   | Traffic-manager version   |
| `DEV_USERD_PROFILING_PORT` | start user daemon with pprof is enabled       |                           |
| `DEV_ROOTD_PROFILING_PORT` | start root daemon with pprof is enabled       |                           |
| `TEST_SUITE`               | Regexp matching test suite name(s)            |                           |

The above environment can optionally be provided in a `itest.yml` file
that is placed adjacent to the normal `config.yml` file used to configure
Telepresence. The `itest.yml` currently has only one single entry, the
`Env` which is a map. It can look something like this:

```yaml
Env:
  DEV_CLIENT_VERSION: v2.22.0-alpha.0
  DEV_KUBECONFIG: /home/thhal/.kube/testconfig
```

## Running integration tests

Integration tests can be run using `go test ./integration_test/...`. For individual tests, use the
`-m.testify=<pattern>` flag. Verbose output using the `-v` flag is also recommended, because the
tests are built with human-readable output in mind and timestamps can be compared to timestamps
found in the telepresence logs.

### Using Docker Desktop with Kubernetes enabled

Using the Kubernetes embedded with Docker is a quick and easy way to run
integration tests, because, when using this setup, there's no need to push
images to a registry. Kubernetes will find them in Docker's local cache
after they have been built.

The integration tests will automatically use `pullPolicy=Never` when the DEV_CLIENT_REGISTRY` is set to
"local", and hence instruct Kubernetes to either find the images in the local cache or fail.
See [local cluster](#run-telepresence-in-a-local-cluster) above.

```bash
export TELEPRESENCE_VERSION=v2.22.0-alpha.0
export DEV_CLIENT_REGISTRY=local
make build client-image tel2-image
go test ./integration_test/... -v -testify.m=Test_InterceptDetailedOutput
```

### Run an individual test:
```bash
go test ./integration_test/... -v -testify.m=Test_InterceptDetailedOutput
```

### Run an integration test suite:
```bash
TEST_SUITE='^WorkloadConfiguration$' go test ./integration_test... -v
```

### Test metric collection

**When running in CI,** `make check-unit` and `make check-integration` the `test-report` tool will
visually modify test output; this happens even running locally, since the json output to go test
is piped to the tool anyway:

```console
$ make check-unit
```

## Building for Release

See https://www.notion.so/datawire/To-Release-Telepresence-2-x-x-2752ef26968444b99d807979cde06f2f

## Updating license documentation

Run `make generate` and commit changes to `DEPENDENCY_LICENSES.md` and `DEPENDENCIES.md`

## Developing on Windows

### Building on Windows

We do not currently support using `make` directly to build on Windows. Instead, use `build-aux\winmake.bat` and pass it the same parameters
you would pass to make. `winmake.bat` will run `make` from inside a Docker container, with appropriate parameters to build windows binaries.

## Debugging and Troubleshooting

### Log output

There are three logs:
- the `connector.log` log file which contains output from the
  background-daemon parts of Telepresence that run as your regular
  user: the interaction with the traffic-manager and the cluster
  (traffic-manager and traffic-agent installs, intercepts, port
  forwards, etc.), and
- the `daemon.log` log file which contains output from the parts of
  telepresence that run as the "root" administrator user: the
  networking changes and services that happen on your workstation.
- the `cli.log` log file which contains output from the command line
  interface.

The location of both logs is:

- on macOS: `~/Library/Logs/telepresence/`
- on GNU/Linux: `~/.cache/telepresence/logs/`
- on Windows `"%USERPROFILE%\AppData\Local\logs"`

The logs are rotating daily.

#### Watching the logs

A convenient way to watch rotating logs is to use `tail -F
<filename>`.  It will automatically and seamlessly follow the
rotation.

#### Debugging early-initialization errors

If there's an error from the connector or daemon during early
initialization, it might quit before the logfiles are set up.  Perhaps
the problem is even with setting up the logfile itself.

You can run the `connector-foreground` or `daemon-foreground` commands
directly, to see what they spit out on stderr before dying:

```console
$ telepresence connector-foreground    # or daemon-foreground
```

If stdout is a TTY device, they don't set up logfiles and instead log
to stderr.  In order to debug the logfile setup, simply pipe the
command to `cat` to trigger the usual logfile setup:

```console
$ telepresence connector-foreground | cat
```

### Profiling the daemons

The daemons can be profiled using [pprof](https://pkg.go.dev/net/http/pprof).
The profiling is initialized using the following flags:

```console
$ telepresence quit -s
$ telepresence connect --userd-profiling-port 6060 --rootd-profiling-port 6061
```

If a daemon is started with pprof, then the goroutine stacks and much other
info can be found by connecting your browser to http://localhost:6060/debug/pprof/
(swap 6060 for whatever port you used with the flags)

#### Dumping the goroutine stacks

A dump will be produced in the respective logs for the daemon simply by killing it
with a SIGQUIT signal. On Windows however, using profiling is the only option.

### RBAC issues

If you are debugging or working on RBAC-related feature work with
Telepresence, it can be helpful to have a user with limited RBAC
privileges/roles.  There are many ways you can do this, but the way we
do it in our tests is like so:

```console
$ kubectl apply -f k8s/client_rbac.yaml
serviceaccount/telepresence-test-developer created
clusterrole.rbac.authorization.k8s.io/telepresence-role created
clusterrolebinding.rbac.authorization.k8s.io/telepresence-clusterrolebinding created

$ kubectl get sa telepresence-test-developer -o "jsonpath={.secrets[0].name}"
telepresence-test-developer-token-<hash>

$ kubectl get secret telepresence-test-developer-token-<hash> -o "jsonpath={.data.token}" > b64_token
$ cat b64_token | base64 --decode
<plaintext token>

$ kubectl config set-credentials telepresence-test-developer --token <plaintext token>
```

This creates a ServiceAccount, ClusterRole, and ClusterRoleBinding
which can be used with kubectl (`kubectl config use-context
telepresence-test-developer`) to work in a RBAC-restricted
environment.
