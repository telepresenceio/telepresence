# Developing Telepresence 2

## Set up your environment

### Development environment

 - `TELEPRESENCE_REGISTRY` (required) is the Docker registry that
   `make push-images` pushes the `tel2` and `telepresence` image to.
   For most developers, the easiest thing is to set it to
   `docker.io/USERNAME`.

 - `TELEPRESENCE_VERSION` (optional) is the "vSEMVER" string to
   compile-in to the binary and Docker image, if set.  Otherwise,
   `make` will automatically set this based on the current Git commit
   and the current time.

 - `DTEST_KUBECONFIG` (optional) is the cluster that is used by tests,
   if set.  Otherwise the tests will automatically use a K3s cluster
   running locally in Docker.  It is not normally necessary to set
   this, but it is useful to set it in order to test against different
   Kubernetes versions/configurations than what
   https://github.com/datawire/dtest uses.

 - `DTEST_REGISTRY` (optional) is the Docker registry that images are
   pushed to by the tests, if set.  Otherwise, the tests will
   automatically use a registry running locally in Docker
   ("localhost:5000").  The tests will push images named `tel2` with
   various version tags.  It is not necessary to set this unless you
   have set `DTEST_KUBECONFIG`.

   If `DTEST_KUBECONFIG` is pointing to a pre-existing cluster, and you
   would like the `DTEST_REGISTRY` to point to a private registry that is
   hosted in that cluster, then you can use `make private-registry`. It
   will deploy a registry and set it up so that it is reachable at
   `localhost:5000`, both from the cluster and from the local workstation.

 - `DEV_TELEPRESENCE_VERSION` (optional) if set to a version such as
   `v2.12.1-alpha.0`, the integration tests will assume that this version
   is pre-built and available, both as a CLI client (accessible from the
   current runtime path), and also pre-pushed into a pre-existing cluster
   accessible from `DTEST_KUBECONFIG`. In other words, if this is set, no
   no binaries will be built or pushed so the development + test cycle
   can be quit rapid.

 - `DEV_CLIENT_IMAGE` (optional) can be set to the fully qualified name of
   an alternative image to use for the docker image used for the containerized
   daemon when running in docker mode.

 - `DEV_MANAGER_IMAGE` (optional) can be set to the fully qualified name of
   an alternative image to use for the traffic manager.

 - `DEV_AGENT_IMAGE` (optional) can be set to the fully qualified name of
   an alternative image to use for the traffic agent.

 - `DEV_USERD_PROFILING_PORT` and `DEV_ROOTD_PROFILING_PORT` (optional) if
   set, will cause the `telepresence connect` calls in the integration tests
   to start daemons where pprof is enabled (see
   [Profiling the daemons](#profiling_the_daemons) below).

The above environment can optionally be provided in a `itest.yml` file
that is placed adjacent to the normal `config.yml` file used to configure
Telepresence. The `itest.yml` currently has only one single entry, the
`Env` which is a map. It can look something like this:

```yaml
Env:
  DEV_TELEPRESENCE_VERSION: v2.12.1-alpha.0
  DTEST_KUBECONFIG: /home/thhal/.kube/testconfig
```

The output of `make help` has a bit more information.

### Running integration tests

Integration tests can be run using `go test ./integration_test/...`. For
individual tests, use the `-m.testify=<pattern>` flag. Verbose output using
the `-v` flag is also recommended, because the tests are built with human
readable output in mind and timestamps can be compared to timestamps found
in the telepresence logs.

Example of running one test with existing cluster and registry:
```
make private-registry
export DTEST_KUBECONFIG=<your kubeconfig>
export DTEST_REGISTRY=localhost:5000
go test ./integration_test/... -v -testify.m=Test_InterceptDetailedOutput
```

If you run these tests on a Mac, localhost won't work. Please use the docker hub, or this value for the registry:

```cli
export DTEST_REGISTRY=host.docker.internal:5000
```

You must also set this in your docker engine settings: 

```json
{
   "insecure-registries": [
     "host.docker.internal:5000"
   ]
}
```

The test takes about a minute to complete when using an existing cluster
and a private registry created by `make private-registry`. During that time
it:
- builds the traffic-manager image
- pushes the image to the registry
- builds the client binary
- creates two namespaces for the test
- performs a helm install of a namespace scoped traffic-manager
- runs the test
- uninstalls the traffic-manager
- deletes the namespaces

The first two can be omitted (and are omitted when the tests run
from CI) by building the binary using `make build`.
Example of running test with existing client and traffic-mananager:

```
make private-registry
export TELEPRESENCE_VERSION=v2.12.1-alpha.0
export TELEPRESENCE_REGISTRY=localhost:5000
make build
make push-images
export DTEST_KUBECONFIG=<your kubeconfig>
export DTEST_REGISTRY=$TELEPRESENCE_REGISTRY
export DEV_TELEPRESENCE_VERSION=$TELEPRESENCE_VERSION

# Run any number of indivitual test with this setup
go test ./integration_test/... -v -testify.m=Test_InterceptDetailedOutput
```

The `DEV_TELEPRESENCE_VERSION` tells the integration test that a client and
a traffic-manager of that version has been prebuilt and pushed. This usually
shortens the time for the test with about 20 seconds.

### Runtime environment

 - The main thing is that in your `~/.config/telepresence/config.yml`
   (`~/Library/Application Support/telepresence/config.yml` on macOS)
   file you set `images.registry` to match the `TELEPRESENCE_REGISTRY`
   environment variable. See
   https://www.getambassador.io/docs/telepresence/latest/reference/config/ 
   for more information.

 - `TELEPRESENCE_VERSION` is is the "vSEMVER" string used by the
   `telepresence` binary *if* one was not compiled in (for example, if
   you're running it with `go run ./cmd/telepresence` rather than
   having built it with `make build`).

 - `TELEPRESENCE_AGENT_IMAGE` is is the "name:vSEMVER" string used when
   the telepresence auto-installs the traffic-manager unless the config.yml
   overrides it by defining `images.agentImage`.

 - You will need have a `~/.kube/config` file (or set `KUBECONFIG` to
   point to a different file) file in order to connect to a cluster;
   same as any other Kubernetes tool.

 - You will need to have [mockgen](https://github.com/golang/mock) installed
   to generate new or updated testing mocks for interfaces.

## Blocking Ambassador telemetry
Telemetry to Ambassador Labs can be disabled by having your os resolve the `metriton.datawire.io` to `127.0.0.1`.

### Windows
`echo "127.0.0.1 metriton.datawire.io" >> c:\windows\system32\drivers\etc\hosts`

### Linux and MacOS
`echo "127.0.0.1 metriton.datawire.io" | sudo tee -a /etc/hosts`

## Build the binary, push the image

The easiest thing to do to get going:

```console
$ TELEPRESENCE_REGISTRY=docker.io/thhal make build push-images # use .\build-aux\winmake.bat build on windows
[make] TELEPRESENCE_VERSION=v2.12.1-19-g37085c2d7-1655891839
... # Lots of output
2.12.1-19-g37085c2d7-1655891839: digest: sha256:40fe852f8d8026a89f196293f37ae8c462c765c85572150d26263d78c43cdd4b size: 1157
```

This has 3 primary outputs:
 1. The `./build-output/bin/telepresence` executable binary
 2. The `${TELEPRESENCE_REGISTRY}/tel2` Docker image
 3. The `${TELEPRESENCE_REGISTRY}/telepresence` Docker image

It essentially does 4 separate tasks:
 1. `make build` to build the `./build-output/bin/telepresence`
    executable binary
 2. `make tel2-image` to build the `${TELEPRESENCE_REGISTRY}/tel2` Docker
    image.
 3. `make client-image` to build the `${TELEPRESENCE_REGISTRY}/telepresence` Docker
   image.
 4. `make push-images` to push the `${TELEPRESENCE_REGISTRY}/tel2` and `${TELEPRESENCE_REGISTRY}/telepresence`
    Docker images.

You can run any of those tasks separately, but be warned: The
`TELEPRESENCE_VERSION` for all 4 needs to agree, and `make` includes a
timestamp in the default `TELEPRESENCE_VERSION`; if you run the tasks
separately you will need to explicitly set the `TELEPRESENCE_VERSION`
environment variable so that they all agree.

When working on just the command-line binary, it is often useful to
run it simply using `go run ./cmd/telepresence` rather than compiling
it first; but be warned: When run this way it won't know its own
version number (`telepresence version` will report "v0.0.0-devel")
unless you set the `TELEPRESENCE_VERSION` environment variable, you
will want to set it to the version of a previously-pushed Docker
image.

You may think that the initial suggestion of running `make build
push-images` all the time (so that every build gets new matching
version numbers) would be terribly slow.  However, This is not as slow
as you might think; both `go` and `docker` are very good about reusing
existing builds and avoiding unnecessary work.

## Run the tests

Running the tests does *not* require having previously built or pushed
anything.

The tests make use of `sudo`; it is useful to get in the habit of
running a no-op `sudo` command to pre-emptively prompt for your
password to avoid having to notice when the prompt appears in the test
output.

```console
$ sudo id
[sudo] password for lukeshu:
uid=0(root) gid=0(root) groups=0(root)

$ make check-unit
[make] TELEPRESENCE_VERSION=v2.6.7-20-g9de10e316-1655892249
...
```

The first time you run the tests, you should use `make check`, to get
`make` to automatically create the requisite `heml` tool
binaries.  However, after that initial run, you can instead use
`gotestsum` or `go test` if you prefer.

### Test metric collection

**When running in CI,** `make check-unit` and `make check-integration` will report the result of test
runs to metriton, Ambassador Labs' metrics store. These reports include test name, running time, and
result. They are reported by the tool at `tools/src/test-report`. This `test-report` tool will also
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

There are two logs:
 - the `connector.log` log file which contains output from the
   background-daemon parts of Telepresence that run as your regular
   user: the interaction with the traffic-manager and the cluster
   (traffic-manager and traffic-agent installs, intercepts, port
   forwards, etc.), and
 - the `daemon.log` log file which contains output from the parts of
   telepresence that run as the "root" administrator user: the
   networking changes and services that happen on your workstation.

The location of both logs is:

 - on macOS: `~/Library/Logs/telepresence/`
 - on GNU/Linux: `~/.cache/telepresence/logs/`
 - on Windows `"%USERPROFILE%\AppData\Local\logs"`

The logs are rotating and a new log is created every time Telepresence
creates a new connection to the cluster, e.g. on `telepresence
connect` after a `telepresence quit` that terminated the last session.

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

### Errors from `make generate`

#### Missing go.sum entries
If you get an error like this:

```
cd tools/src/go-mkopensource && GOOS= GOARCH= go build -o /home/andres/source/production/telepresence/tools/bin/go-mkopensource $(sed -En 's,^import "(.*)".*,\1,p' pin.go)
missing go.sum entry for module providing package github.com/datawire/go-mkopensource; to add:
	go mod download github.com/datawire/go-mkopensource
```

Add the missing entries by going to the folder that caused the failure (in this case it's
/home/andres/source/production/telepresence/tools/bin/go-mkopensource) and run the command provided by go:

```
go mod download github.com/datawire/go-mkopensource
```
