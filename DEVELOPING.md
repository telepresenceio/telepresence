# Developing Telepresence 2

## Set up your environment

### Development environment

 - `TELEPRESENCE_REGISTRY` (required) is the Docker registry that
   `make push-image` pushes the `tel2` image to, and that the
   `tel2-base` image is fetch-from/pushed-to as needed.  For most
   developers the easiest thing is to set it to `docker.io/USERNAME`.

 - `TELEPRESENCE_VERSION` (optional) is the "vSEMVER" string to
   compile-in to the binary and Docker image, if set.  Otherwise,
   `make` will automatically set this based on the current Git commit
   and the current time.

 - `DTEST_KUBECONFIG` (optional) is the cluster that is used by tests,
   if set.  Otherwise the tests will automatically use a K3s cluster
   running locally in Docker.  It is not normally nescessary to set
   this, but it is useful to set it in order to test against different
   Kubernetes versions/configurations than what
   https://github.com/datawire/dtest uses.

 - `DTEST_REGISTRY` (optional) is the Docker registry that images are
   pushed to by the tests, if set.  Otherwise, the tests will
   automatically use a registry running locally in Docker
   ("localhost:5000").  The tests will push images named `tel2` and
   `tel2-base` with various version tags.  It is not nescessary to set
   this unless you have set `DTEST_KUBECONFIG`.

The output of `make help` has a bit more information.

### Runtime environment

 - The main thing is that in your `~/.config/telepresence/config.yml`
   (`~/Library/Application Support/telepresence/config.yml` on macOS)
   file you set `images.registry` to match the `TELEPRESENCE_REGISTRY`
   environment variable. See
   https://www.telepresence.io/docs/latest/reference/config/ for more
   information.

 - `TELEPRESENCE_VERSION` is is the "vSEMVER" string used by the
   `telepresence` binary *if* one was not compiled in (for example, if
   you're running it with `go run ./cmd/telepresence` rather than
   having built it with `make build`).

 - You will need have a `~/.kube/config` file (or set `KUBECONFIG` to
   point to a different file) file in order to connect to a cluster;
   same as any other Kubernetes tool.

## Build the binary, push the image

The easiest thing to do to get going:

```console
$ TELEPRESENCE_REGISTRY=docker.io/lukeshu make build push-image
[make] TELEPRESENCE_VERSION=v2.3.5-63-ga9c8c660-1626378362

mkdir -p build-output/bin
CGO_ENABLED=0 go build -trimpath -ldflags=-X=github.com/telepresenceio/telepresence/v2/pkg/version.Version=v2.3.5-63-ga9c8c660-1626378362 -o build-output/bin ./cmd/...

if ! docker pull docker.io/lukeshu/tel2-base:0d118a970c93bb1387fce6bb5638e86eb82d2cbe; then \
  cd base-image && docker build --pull -t docker.io/lukeshu/tel2-base:0d118a970c93bb1387fce6bb5638e86eb82d2cbe . && \
  docker push docker.io/lukeshu/tel2-base:0d118a970c93bb1387fce6bb5638e86eb82d2cbe; \
fi
0d118a970c93bb1387fce6bb5638e86eb82d2cbe: Pulling from lukeshu/tel2-base
Digest: sha256:3d557f4abf033990c4c165639e37123bee8c482471b034f53a1cbb407e94d9da
Status: Image is up to date for lukeshu/tel2-base:0d118a970c93bb1387fce6bb5638e86eb82d2cbe
docker.io/lukeshu/tel2-base:0d118a970c93bb1387fce6bb5638e86eb82d2cbe
sed  -e 's|@TELEPRESENCE_REGISTRY@|docker.io/lukeshu|g'  -e 's|@TELEPRESENCE_BASE_VERSION@|0d118a970c93bb1387fce6bb5638e86eb82d2cbe|g' <.ko.yaml.in >.ko.yaml
localname=$(GOFLAGS="-ldflags=-X=github.com/telepresenceio/telepresence/v2/pkg/version.Version=v2.3.5-63-ga9c8c660-1626378362 -trimpath" ko publish --local ./cmd/traffic) && \
docker tag "$localname" docker.io/lukeshu/tel2:2.3.5-63-ga9c8c660-1626378362
2021/07/15 13:46:06 Using base docker.io/lukeshu/tel2-base:0d118a970c93bb1387fce6bb5638e86eb82d2cbe for github.com/telepresenceio/telepresence/v2/cmd/traffic
2021/07/15 13:46:07 Building github.com/telepresenceio/telepresence/v2/cmd/traffic for linux/amd64
2021/07/15 13:46:13 Loading ko.local/traffic-583382724d65cac88dce46a7e0490a6c:b182c8fe7541da86bc26c758648badd534973c27017855f2bdd2cee3d0dd615d
2021/07/15 13:46:15 Loaded ko.local/traffic-583382724d65cac88dce46a7e0490a6c:b182c8fe7541da86bc26c758648badd534973c27017855f2bdd2cee3d0dd615d
2021/07/15 13:46:15 Adding tag latest
2021/07/15 13:46:16 Added tag latest

docker push docker.io/lukeshu/tel2:2.3.5-63-ga9c8c660-1626378362
The push refers to repository [docker.io/lukeshu/tel2]
07cc3e3258f3: Pushed
4f76c7cc1547: Layer already exists
3aae10101f20: Layer already exists
7f202b5d8654: Layer already exists
b2d5eeeaba3a: Layer already exists
2.3.5-63-ga9c8c660-1626378362: digest: sha256:d6e429d53cff2293f5bfc764db33664a6f03f78b65b2798f33354f76c40f46a7 size: 1363
```

This has 2 primary outputs:
 1. The `./build-output/bin/telepresence` executable binary
 2. The `${TELEPRESENCE_REGISTRY}/tel2` Docker image

It essentially does 3 separate tasks:
 1. `make build` to build the `./build-output/bin/telepresence`
    executable binary
 2. `make image` to build the `${TELEPRESENCE_REGISTRY}/tel2` Docker
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
as you might think; both `go` and `ko` are very good about reusing
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

$ make check
[make] TELEPRESENCE_VERSION=v2.3.5-63-gbaf1db29-1626379548

cd tools/src/ko && go build -o /home/lukeshu/src/github.com/telepresenceio/telepresence-x/tools/bin/ko $(sed -En 's,^import "(.*)"$,\1,p' pin.go)

mkdir -p tools
curl -sfL https://get.helm.sh/helm-v3.5.4-linux-amd64.tar.gz -o tools/helm-v3.5.4-linux-amd64.tar.gz
mkdir -p tools/bin
tar -C tools/bin -zxmf tools/helm-v3.5.4-linux-amd64.tar.gz --strip-components=1 linux-amd64/helm

go test -timeout=15m ./...
?       github.com/telepresenceio/telepresence/v2/build-aux     [no test files]
?       github.com/telepresenceio/telepresence/v2/cmd/telepresence      [no test files]
?       github.com/telepresenceio/telepresence/v2/cmd/traffic   [no test files]
ok      github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/agent (cached)
ok      github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager       (cached)
?       github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/internal/cluster      [no test files]
ok      github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/internal/mutator      (cached)
ok      github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/internal/state        (cached)
?       github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/internal/test [no test files]
ok      github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/internal/watchable    (cached)
ok      github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil   (cached)
ok      github.com/telepresenceio/telepresence/v2/pkg/client    (cached)
?       github.com/telepresenceio/telepresence/v2/pkg/client/actions    [no test files]
?       github.com/telepresenceio/telepresence/v2/pkg/client/cache      [no test files]
ok      github.com/telepresenceio/telepresence/v2/pkg/client/cli        580.873s
?       github.com/telepresenceio/telepresence/v2/pkg/client/cli/cliutil        [no test files]
ok      github.com/telepresenceio/telepresence/v2/pkg/client/cli/extensions     (cached)
?       github.com/telepresenceio/telepresence/v2/pkg/client/connector  [no test files]
?       github.com/telepresenceio/telepresence/v2/pkg/client/connector/internal/broadcastqueue  [no test files]
?       github.com/telepresenceio/telepresence/v2/pkg/client/connector/internal/scout   [no test files]
?       github.com/telepresenceio/telepresence/v2/pkg/client/connector/sharedstate      [no test files]
ok      github.com/telepresenceio/telepresence/v2/pkg/client/connector/userd_auth       (cached)
?       github.com/telepresenceio/telepresence/v2/pkg/client/connector/userd_auth/authdata      [no test files]
?       github.com/telepresenceio/telepresence/v2/pkg/client/connector/userd_grpc       [no test files]
?       github.com/telepresenceio/telepresence/v2/pkg/client/connector/userd_k8s        [no test files]
ok      github.com/telepresenceio/telepresence/v2/pkg/client/connector/userd_trafficmgr 615.512s
?       github.com/telepresenceio/telepresence/v2/pkg/client/daemon     [no test files]
?       github.com/telepresenceio/telepresence/v2/pkg/client/daemon/dbus        [no test files]
?       github.com/telepresenceio/telepresence/v2/pkg/client/daemon/dns [no test files]
ok      github.com/telepresenceio/telepresence/v2/pkg/client/logging    (cached)
?       github.com/telepresenceio/telepresence/v2/pkg/connpool  [no test files]
?       github.com/telepresenceio/telepresence/v2/pkg/dnet      [no test files]
?       github.com/telepresenceio/telepresence/v2/pkg/dpipe     [no test files]
ok      github.com/telepresenceio/telepresence/v2/pkg/filelocation      (cached)
?       github.com/telepresenceio/telepresence/v2/pkg/forwarder [no test files]
?       github.com/telepresenceio/telepresence/v2/pkg/install   [no test files]
?       github.com/telepresenceio/telepresence/v2/pkg/install/resource  [no test files]
?       github.com/telepresenceio/telepresence/v2/pkg/iputil    [no test files]
ok      github.com/telepresenceio/telepresence/v2/pkg/subnet    (cached)
?       github.com/telepresenceio/telepresence/v2/pkg/systema   [no test files]
?       github.com/telepresenceio/telepresence/v2/pkg/systema/internal/loopback [no test files]
?       github.com/telepresenceio/telepresence/v2/pkg/tun       [no test files]
?       github.com/telepresenceio/telepresence/v2/pkg/tun/buffer        [no test files]
?       github.com/telepresenceio/telepresence/v2/pkg/tun/icmp  [no test files]
?       github.com/telepresenceio/telepresence/v2/pkg/tun/ip    [no test files]
?       github.com/telepresenceio/telepresence/v2/pkg/tun/tcp   [no test files]
?       github.com/telepresenceio/telepresence/v2/pkg/tun/udp   [no test files]
?       github.com/telepresenceio/telepresence/v2/pkg/version   [no test files]
```

The first time you run the tests, you should use `make check`, to get
`make` to automatically create the requisite `ko` and `heml` tool
binaries.  Howver, after that initial run, you can isntead use
`gotestsum` or `go test` if you prefer.

### I've made a change to the agent-installer, how do I update the testdata output files?

If you've made a change to the agent-installer that requires updating
the `testadata/addAgentToWorkload/*.output.yaml` files, it can be
tedious to update each file separately.

If you set the `DEV_TELEPRESENCE_GENERATE_GOLD` environment variable
to a non-empty value, and run the test again, it will update the files
based on the current behavior (the test will still fail that first
run, though).  Be sure to look at the diff and make sure that new
behavior is actually correct!

```console
$ DEV_TELEPRESENCE_GENERATE_GOLD=y go test -run=TestAddAgentToWorkload ./pkg/client/connector/userd_trafficmgr
```

## Building for Release

See https://www.notion.so/datawire/To-Release-Telepresence-2-x-x-2752ef26968444b99d807979cde06f2f

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

When running `make generate` you may hit errors that look like:

```
./generic.gen: line 23: generated_${MAPTYPE,,}.go: bad substitution
./generic.gen: line 37: generated_${MAPTYPE,,}_test.go: bad substitution
cmd/traffic/cmd/manager/internal/watchable/generic.go:1: running "./generic.gen": exit status 1
```

To fix them, ensure that you're running `bash` 4.0 (2009) or newer.
In MacOS this can be done installing it from Homebrew:

```bash
brew install bash
```
