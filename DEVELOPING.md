# Developing Telepresence 2

## Set up your environment

- `KUBECONFIG` needs to be set to a working cluster
- `DEV_KUBECONFIG` is the cluster that is used by tests, if set. Otherwise the tests will use a K3s cluster running in Docker.
- These registry variables should all point to the same registry for now. We'll clean this up in the near future. If you can push to `gcr.io` then that's the cheapest; otherwise something like `docker.io/ark3` will work fine. The `localhost:5000` DTest registry works fine too if you're using the DTest cluster.
  - `DEV_REGISTRY` sets things up for the tests
  - `TELEPRESENCE_REGISTRY` is used by the Tel binary to set the image it uses when adding or modifying the cluster
  - `KO_DOCKER_REPO` is used by the image build -- this may already be unnecessary
- `VERSION_SUFFIX` defaults to a value that includes a timestamp at the end of the version number at build. You can set it to the empty string to remove that suffix if you're comfortable that your cluster will pull the latest image when appropriate.

The output of `make help` shows some of this information, but not all of it, and it is not quite correct at the moment. We will improve all of this soon.


## Build the binaries

```console
$ make build
mkdir -p build-output/bin
go build -ldflags=-X=github.com/datawire/telepresence2/pkg/version.Version=v0.2.0-1605793571 -o build-output/bin ./cmd/...

$ ./build-output/bin/telepresence version
Client v0.2.0-1605793571 (api v3)
```

You can also use `go run ./cmd/telepresence` et al during development, but be aware that the binary will not know its version number.

The Telepresence binary uses the `TELEPRESENCE_REGISTRY` and `TELEPRESENCE_VERSION` environment variables to compute the name and tag of the image it will use when it modifies your cluster (e.g., to add a Traffic Manager), falling back to `docker.io/datawire` and its compiled-in version number respectively if those variables are unset.


## Run the tests

```console
$ sudo id
uid=0(root) gid=0(root) groups=0(root)

$ make check
go test -v ./...
?       github.com/datawire/telepresence2/cmd/telepresence      [no test files]
?       github.com/datawire/telepresence2/cmd/traffic   [no test files]
=== RUN   TestState_HandleIntercepts
--- PASS: TestState_HandleIntercepts (0.01s)
PASS
ok      github.com/datawire/telepresence2/pkg/agent     (cached)
?       github.com/datawire/telepresence2/pkg/client    [no test files]
=== RUN   TestTelepresence
Running Suite: Telepresence Suite
=================================
Random Seed: 1605793648
Will run 9 of 9 specs

•••••••••
Ran 9 of 9 Specs in 47.723 seconds
SUCCESS! -- 9 Passed | 0 Failed | 0 Pending | 0 Skipped
--- PASS: TestTelepresence (47.72s)
PASS
ok      github.com/datawire/telepresence2/pkg/client/cli        47.756s
+ kubectl --kubeconfig /tmp/dtest-kubeconfig-ark3-d4942b3f89a9.yaml create namespace telepresence-349112
=== RUN   Test_findTrafficManager_notPresent
--- PASS: Test_findTrafficManager_notPresent (0.01s)
=== RUN   Test_findTrafficManager_present
+ ko publish --local ./cmd/traffic
+ docker tag ko.local/traffic-6c3ca0a9c236a15e275ec10cceb31334:36ff29b03fdc379899eb7652fa8655f3c26590f96319af0ba90b73d938c8b99e localhost:5000/tel2:v0.1.2-test
+ docker push localhost:5000/tel2:v0.1.2-test
--- PASS: Test_findTrafficManager_present (4.09s)
=== RUN   Test_ensureTrafficManager_notPresent
--- PASS: Test_ensureTrafficManager_notPresent (0.17s)
PASS
ok      github.com/datawire/telepresence2/pkg/client/connector  52.269s
?       github.com/datawire/telepresence2/pkg/client/daemon     [no test files]
?       github.com/datawire/telepresence2/pkg/client/daemon/dns [no test files]
2020/11/19 08:48:20 Acquiring machine lock "default" took 50.24 seconds
=== RUN   TestTranslator
--- PASS: TestTranslator (3.54s)
=== RUN   TestSorted
--- PASS: TestSorted (0.01s)
PASS
ok      github.com/datawire/telepresence2/pkg/client/daemon/nat 55.759s
?       github.com/datawire/telepresence2/pkg/client/daemon/proxy       [no test files]
=== RUN   TestMechanismHelpers
--- PASS: TestMechanismHelpers (0.00s)
=== RUN   TestAgentHelpers
--- PASS: TestAgentHelpers (0.00s)
=== RUN   TestPresence
--- PASS: TestPresence (0.00s)
=== RUN   TestConnect
--- PASS: TestConnect (0.00s)
=== RUN   TestStateInternal
=== RUN   TestStateInternal/agents
=== RUN   TestStateInternal/presence-redundant
--- PASS: TestStateInternal (0.00s)
    --- PASS: TestStateInternal/agents (0.00s)
    --- PASS: TestStateInternal/presence-redundant (0.00s)
=== RUN   TestWatches
    watches_test.go:26: Skipped! Use "env CI=true go test [...]" to run
--- SKIP: TestWatches (0.00s)
PASS
ok      github.com/datawire/telepresence2/pkg/manager   (cached)
?       github.com/datawire/telepresence2/rpc       [no test files]
?       github.com/datawire/telepresence2/rpc/connector     [no test files]
?       github.com/datawire/telepresence2/rpc/daemon        [no test files]
?       github.com/datawire/telepresence2/rpc/iptables      [no test files]
?       github.com/datawire/telepresence2/rpc/version       [no test files]
?       github.com/datawire/telepresence2/pkg/version   [no test files]
```

You can also use `gotestsum` or manually run `go test` as you prefer.


## Build the image

```console
$ make image
2020/11/19 08:57:58 Using base docker.io/datawire/tel2-base:20201105 for github.com/datawire/telepresence2/cmd/traffic
2020/11/19 08:57:58 Building github.com/datawire/telepresence2/cmd/traffic for linux/amd64
2020/11/19 08:57:59 Loading ko.local/traffic-6c3ca0a9c236a15e275ec10cceb31334:2f96b389da4fb4a3ae249e55293c3608fb8d9a9cf979d534bea7ace69e853ce0
2020/11/19 08:58:01 Loaded ko.local/traffic-6c3ca0a9c236a15e275ec10cceb31334:2f96b389da4fb4a3ae249e55293c3608fb8d9a9cf979d534bea7ace69e853ce0
2020/11/19 08:58:01 Adding tag latest
2020/11/19 08:58:01 Added tag latest
docker tag ko.local/traffic-6c3ca0a9c236a15e275ec10cceb31334:2f96b389da4fb4a3ae249e55293c3608fb8d9a9cf979d534bea7ace69e853ce0 docker.io/ark3/tel2:v0.2.0-1605794277
```

The image is now in your machine's Docker daemon and tagged as shown. You can push that image manually, thus allowing your Kube cluster to use it.

```console
$ docker push docker.io/ark3/tel2:v0.2.0-1605794277
The push refers to repository [docker.io/ark3/tel2]
41e7a1ba6d29: Pushed
4f76c7cc1547: Mounted from datawire/tel2
3a3c83bc9ed4: Mounted from datawire/tel2
7e2446562a4e: Mounted from datawire/tel2
721384ec99e5: Mounted from ark3/telepresence-local
v0.2.0-1605794277: digest: sha256:f9b26f48659748fea4977cf4664233eb4b98bf6861bfd46033b3c309d34cb6fd size: 1363
```

**Note**: If you've been following along, your Telepresence binary and your Telepresence image have _different versions_: the timestamp will (almost certainly) be different. This means if you run Telepresence against an empty cluster, the image it sets for the Traffic Manager will not exist with the tag it specifies.

During your dev loop you can work around this using any of these methods:
- Set `TELEPRESENCE_VERSION` manually to the image's version number. Update that value only when you rebuild the image.
- Set `VERSION_SUFFIX` to a fixed string so that there is no constantly-changing timestamp.
- Always run `make build image` so that everything has the same version number, and then `docker push` the image every time. This is not as slow as you might think; both `go` and `ko` are very good about reusing existing builds and avoiding unnecessary work.
- Have your dev loop revolve around `make check`, which does the correct building, tagging, etc. automatically.

In practice, this is not a big deal. If you get the version numbers correct once and deploy things to the cluster, you can then use Telepresence with a diverging version number against the existing cluster components and they will work fine. This will be most problematic when you need to update the image itself frequently.

In the long run we'll improve this to work more like classic Telepresence, but even there this is not a fully-solved problem. And we can't really use Telepresence to develop Telepresence. At least not yet.


## Building for Release

1. Add a `vSEMVER` tag for the new version: `git tag -a v0.x.y -m "Release 0.x.y"`
2. Push the tag to GitHub: `git push origin v0.x.y`
3. Wait for CI to run
