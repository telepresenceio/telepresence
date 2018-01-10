# Development info

### Known issues

* Docs get updated off of master, so documentation on site may reference unreleased features if you're not careful.
  Until that's fixed, releases should happen as soon as new feature is merged to master.
  See [issue #4](https://github.com/datawire/telepresence/issues/4).
* Mac Travis build relies on Docker images pushed from Linux build; we're just assuming Mac builds will always be slower than Linux builds.
  Travis now supports staged builds so we should switch to that eventually.
  See [issue #153](https://github.com/datawire/telepresence/issues/153).

### Setting up a development environment

The following instructions will gets the Telepresence source and sets up some of its dependencies (torsocks, gcloud).
It also creates a virtualenv and installs Telepresence's Python dependencies into it.
The arguments required for `environment-setup.sh` are Google Cloud configuration items which identify a GKE cluster which can be used for testing.


```console
$ git clone git@github.com:datawire/telepresence.git
$ cd telepresence
$ ./environment-setup.sh $PROJECT $CLUSTER $ZONE
$ ./build --manage-virtualenv
```
You may want to activate the virtualenvenv (for the duration of your shell):

```console
$ . virtualenv/bin/activate
```

This will give you access to the Telepresence executables:

* `telepresence`
* `stamp-telepresence`
* `sshuttle-telepresence`.

If you modify Telepresence, you must rebuild the Docker images and make them available to be pulled from elsewhere.

```console
$ ./build --registry <Docker registry for tag and push> --build-and-push
```

Or if you want to build images using minikube:

```console
$ eval $(minikube docker-env --shell bash)
$ ./build --registry <Docker registry for tag and push> --build-and-push
```

Or using minishift:

```console
$ eval $(minishift docker-env --shell bash)
$ ./build --registry <Docker registry for tag and push> --build-and-push
```

### Coding standard

Formatting is enforced by the installed `yapf` tool; to reformat the code, you can do:

```console
$ virtualenv/bin/yapf -r -i telepresence
```

### Releasing Telepresence

Theory of operation:

1. Make sure `docs/reference/changelog.md` has changelog entries for next release, and today's release date.
2. Use [bumpversion](https://pypi.python.org/pypi/bumpversion) to increase the version in relevant files and then commit a new git tag with the new version.
   See `.bumpversion.cfg` for the configuration.
3. Push the new commit and tag to GitHub.
4. This will trigger Travis CI, which will in turn:
   1. Push a new Docker image to the Docker Hub.
   2. Update the Homebrew formula in [homebrew-blackbird](https://github.com/datawire/homebrew-blackbird).
      The Homebrew formula refers to the tarball GitHub [generates for each release](https://github.com/datawire/telepresence/releases).
   3. Upload .deb and .rpm files to https://packagecloud.io.

The corresponding commands for steps 2-4 are:

```
make bumpversion
git push origin master --tags
```

Then check https://travis-ci.org/datawire/telepresence/branches, and specifically the build for the new tag (e.g. `0.63`).

### Running tests

#### Full test suites

In order to run *all* possible code paths in Telepresence, you need to do the following:

| Test environment   | How to run                                           |
|--------------------|------------------------------------------------------|
| Minikube           | `make minikube-test`                                 |
| Remote K8s cluster | Runs on Travis                                       |
| Minishift          | `make openshift-tests` with minishift kube context   |
| Remote OS cluster  | `make openshift-tests` with remote OpenShift context |
| Docker on Mac      | `make minikube-test` on Mac with Docker              |
| Other Mac          | Runs on Travis                                       |

In practice running on remote OpenShift cluster usually doesn't happen.

Travis on Mac cannot support Docker, which is why that needs to be done manually.

#### Running individual tests

When doing local development you will typically run all tests by doing:

> `make minikube-test`

If you want to only run some tests you can pass arguments to the underlying `py.test` run using `TELEPRESENCE_TESTS`.
For example, to run all tests containing the string "fromcluster" and to exit immediately after first failed test:

> `TELEPRESENCE_TESTS="-x -k fromcluster" make minikube-test`

See `py.test --help` for other options you might want to set in `TELEPRESENCE_TESTS`.

### Running a local copy of `telepresence`

FIXME: This is out-of-date. The above section of setting up a development environment has the correct info, but lacks a clear example like this section has.

During local development, typically against minikube, you will want to manually run `telepresence` you are working on.
You need to:

1. Make sure `minikube` has latest server-side Docker image: `make build-k8s-proxy-minikube` will do this. It has issues on Mac, however, due to old version of `make` maybe? Read the `Makefile` to see what it does.
   If you forget this step you will have problems with Minikube not finding the Docker image for `telepresence-k8s`.
2. If you're using `--docker-run`, your local Docker needs to have the latest Docker image: `make build-local`.
3. You need to run `cli/telepresence` with env variable telling it the version number it should be using; this will be used as the tag for Docker images you created in steps 1 and 2. You do this by setting `TELEPRESENCE_VERSION` to the output of `make version`. You also need to set `PATH` so `sshuttle-telepresence` is found.

For example:

```console
$ cli/telepresence --version
0.61

$ make version
0.61-1-gadd8818

$ make build-k8s-proxy-minikube
...

$ env PATH=$PATH:$PWD/virtualenv/bin/ TELEPRESENCE_VERSION=$(make version) \
  cli/telepresence --version
0.61-1-gadd8818

$ env PATH=$PATH:$PWD/virtualenv/bin/ TELEPRESENCE_VERSION=$(make version) \
  cli/telepresence --run-shell
@minikube|$ ...
```
