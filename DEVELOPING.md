# Developing Telepresence 2

## Set up your environment

### Development environment

 - `TELEPRESENCE_REGISTRY` (required) is the Docker registry that
   `make push-image` pushes the `tel2` image to.  For most developers
   the easiest thing is to set it to `docker.io/USERNAME`.

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

 - `DEV_TELEPRESENCE_VERSION` (optional) if set to a version such as
   `v2.6.7-alpha.0`, the integration tests will assume that this version
   is pre-built and available, both as a CLI client (accessible from the
   current runtime path), and also pre-pushed into a pre-existing cluster
   accessible from `DTEST_KUBECONFIG`. In other words, if this is set, no
   no binaries will be built or pushed so the developement + test cycle
   can be quit rapid.

 - `DEV_AGENT_IMAGE` (optional) can be set to an alternative image to use
   for the traffic agent, such as `ambassador-telepresence-agent:1.12.7-alpha.0`.
   This will make all tests use that traffic-agent instead of the default
   which uses the same image as the traffic-manager.

The output of `make help` has a bit more information.

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
Telemetry from dev builds to Ambassador Labs can be disabled by having your os resolve the `metriton.datawire.io` to `127.0.0.1`.

### Windows
`echo "127.0.0.1 metriton.datawire.io" >> c:\windows\system32\drivers\etc\hosts`

### Linux and MacOS
`echo "127.0.0.1 metriton.datawire.io" | sudo tee -a /etc/hosts`

## Build the binary, push the image

The easiest thing to do to get going:

```console
$ TELEPRESENCE_REGISTRY=docker.io/lukeshu make build push-images # use .\build-aux\winmake.bat build on windows
$ TELEPRESENCE_REGISTRY=docker.io/thhal make build push-images # use .\build-aux\winmake.bat build on windows
[make] TELEPRESENCE_VERSION=v2.6.7-19-g37085c2d7-1655891839
... # Lots of output
2.6.7-19-g37085c2d7-1655891839: digest: sha256:40fe852f8d8026a89f196293f37ae8c462c765c85572150d26263d78c43cdd4b size: 1157
```

This has 2 primary outputs:
 1. The `./build-output/bin/telepresence` executable binary
 2. The `${TELEPRESENCE_REGISTRY}/tel2` Docker image

It essentially does 3 separate tasks:
 1. `make build` to build the `./build-output/bin/telepresence`
    executable binary
 2. `make tel2` to build the `${TELEPRESENCE_REGISTRY}/tel2` Docker
    image.
 3. `make push-image` to push the `${TELEPRESENCE_REGISTRY}/tel2`
    Docker image.

You can run any of those tasks separately, but be warned: The
`TELEPRESENCE_VERSION` for all 3 needs to agree, and `make` includes a
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
push-image` all the time (so that every build gets new matching
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

$ make make check-unit
[make] TELEPRESENCE_VERSION=v2.6.7-20-g9de10e316-1655892249
...
```

The first time you run the tests, you should use `make check`, to get
`make` to automatically create the requisite `heml` tool
binaries.  However, after that initial run, you can instead use
`gotestsum` or `go test` if you prefer.

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