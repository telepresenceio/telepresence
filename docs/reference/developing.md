# Development Guide

### Known issues

* Docs get updated off of master, so documentation on site may reference unreleased features if you're not careful.
  Until that's fixed, releases should happen as soon as new feature is merged to master.
  See [issue #4](https://github.com/telepresenceio/telepresence/issues/4).

### Setting up a development environment

The following instructions will gets the Telepresence source and sets up some of its dependencies (torsocks, gcloud).
It also creates a virtualenv and installs Telepresence's Python dependencies into it.
The arguments required for `environment-setup.sh` are Google Cloud configuration items which identify a GKE cluster which can be used for testing, plus the operating system.


```console
$ git clone git@github.com:telepresenceio/telepresence.git
$ cd telepresence
$ ./environment-setup.sh $PROJECT $CLUSTER $ZONE <linux|osx>
$ make virtualenv
```

You may want to activate the virtualenv (for the duration of your shell):

```console
$ . virtualenv/bin/activate
```

This will give you access to the Telepresence executables:

* `telepresence`
* `sshuttle-telepresence`.

You can test your modifications to Telepresence with the `build` tool:

```console
$ make check TELEPRESENCE_REGISTRY=<Docker registry for tag and push> [PYTEST_ARGS=<pytest args>]
```

You can run a subset of the tests using the pytest features for selecting tests
(for example, `-k` and `-m`).
End-to-end tests are marked with the method and operation they exercise.
So, for example, you can run all of the tests in the vpn-tcp, swap-deployment configuration:

```console
$ make check TELEPRESENCE_REGISTRY=<Docker registry for tag and push> PYTEST_ARGS="-m 'vpn_tcp and swap_deployment'"
```

Note that `-` must be replaced with `_` due to pytest limitations.

See `make help` for details about how to run specific tests.

You can also build images and push them to a registry without running any tests:

```console
$ make docker-push TELEPRESENCE_REGISTRY=<Docker registry for tag and push>
```

If you want to push images to the local registry, start the container first:

```console
$ docker run -d -p 5000:5000 --restart=always --name docker-local-registry registry
```

and then simply use `TELEPRESENCE_REGISTRY=localhost:5000`.

Or if you want to build images using minikube (untested):

```console
$ eval $(minikube docker-env --shell bash)
$ make docker-push TELEPRESENCE_REGISTRY=<Docker registry for tag and push>
```

Or using minishift (untested):

```console
$ eval $(minishift docker-env --shell bash)
$ make docker-push TELEPRESENCE_REGISTRY=<Docker registry for tag and push>
```

### End-to-End Testing

The Telepresence test suite includes a set of tests which run Telepresence as a real user would.
These tests launch Telepresence and have it communicate with a real Kubernetes cluster, running real pods and observing the results.
These tests are implemented in `tests/test_endtoend.py` and `tests/test_endtoend_distinct.py`.

While the test functions themselves are present in these test modules,
there are several additional support modules also involved.
`tests/probe_endtoend.py` is a Python program which the tests tell Telepresence to run.
`tests/parameterize_utils.py` is a support module for writing tests.
`tests/conftest.py` integrates the tests with pytest.
At points during end-to-end test development you may find yourself working with any of these sources.

With the aim of making it clear how to write your own end-to-end test, here is one dissected.

#### Test Probe

```python
from .conftest import (
    with_probe,
)

@with_probe
def test_demonstration(probe):
```

The end-to-end tests are written using a number of pytest features.
The first is parameterized fixtures to make it easy to apply a test to all Telepresence execution modes.

Notice that the test function is defined to take an argument `probe`.
The argument must be named `probe` to select the correct pytest fixture.
The `with_probe` decorator parameterizes the `probe` fixture with all of the Telepresence execution modes.
This means that pytest will call this test function many times with different values for `probe`.
For example, the test function will be called with a probe associated with a run of Telepresence given the `--method=container --new-deployment` arguments.
The test function is called once for each combination of method arguments and operations (`--new-deployment`, `--swap-deployment`, etc).


#### Probe Result

```python
    probe_environment = probe.result().result["environ"]
```

`probe.result()` will be used in every end-to-end test.
This method returns an object - a `ProbeResult` - representing the Telepresence run.
This *may* initiate a new run of Telepresence but it may also re-use the Telepresence launched by an earlier test with the same configuration (the same `probe`).
This is the result of pytest fixture optimization and it allows the test suite to run Telepresence far fewer times than would otherwise be required (reducing the overall runtime of the test suite).

The Telepresence probe collects some information from the Telepresence execution context immediately upon starting.
The `result` attribute of the `ProbeResult` provides access to this information.
In this case, we retrieve the complete POSIX environment for inspection.

#### Probe Operation

```python
    if probe.operation.inherits_deployment_environment():
```

This test now prepares to make its first assertion.
This first assertion is guarded by a check against the result of a method of `probe.operation`.
`probe.operation` is a reference to an object representing the operation which the probe used.
Remember that a test decorated with `with_probe` will be run multiple times with different `probe` arguments.
Many of those probes will be configured with a different operation.
This attribute lets a test vary its behavior based on that operation.
This is useful because different operations may have different desired behavior and require different assertions in their tests.
For more details about what can be done with the operation object, see `tests/parameterize_utils.py` where operations are implemented.

In this case, `inherits_deployment_environment` is a method of the operation which returns a boolean.
The result indicates whether it is expired and desired that the Telepresence execution context's POSIX environment inherits environment variables that were set in a pre-existing Kubernetes Deployment resource.
Not all Telepresence configurations interact with a pre-existing Deployment - hence the need for this check.

Supposing we are running with a probe where this check succeeds:

```python
    desired = probe.DESIRED_ENVIRONMENT
    expected = {
        k: probe_environment.get(k, None)
        for k in probe.DESIRED_ENVIRONMENT
    }
    assert desired == expected
```

Here the test makes an assertion about the observed POSIX environment.
It looks up the value of the environment which *ought* to have been inherited - `probe.DESIRED_ENVIRONMENT` - and makes sure the items all appear in the observed environment.

For the `else` case of this branch, we might assert that `desired` does *not* appear in the observed environment.

#### Probe Method

Test can also inspect the method in use.

```python
    if probe.method.inherits_client_environment():
        assert probe.CLIENT_ENV_VAR in probe_environment
```

The idea here is similar.
Different behavior may be desired from different methods.
Inspection of `probe.method` provides a way to vary the test behavior based on this.
Methods are implemented in `tests/parameterize_utils.py`.

#### Probe Interaction

The Telepresence process associated with the probe continues to run while the tests run.
This means interactions with it are possible.
Simple messages can easily be exchanged with the probe.

```python
    probe_result.write("probe-also-proxy {}".format(hostname))
    success, request_ip = loads(probe_result.read())
```

This uses a command supported by the probe which makes it issue an HTTP request to a particular URL and return the response.
These commands are implemented in `probe_endtoend.py`.
In this case, the result is a two-tuple.
The first element indicates whether the HTTP request succeeded or not.
The second element gives some data from the HTTP response (if it succeeded).

Probe commands are useful for observing any state or behavior which is only visible in the Telepresence execution context.
They allow the test to retrieve the information so assertions can be made.

#### Final Thoughts

When writing end-to-end tests keep a few things in mind:

##### Shared Telepresence

The `probe` fixture re-uses `Probe` instances.
Tests should not modify the `Probe` passed in for the `probe` argument.
Doing so will invalidate the results of subsequent tests.

Likewise, the `Probe` instance has an associated `Telepresence` process.
Tests should not modify that process, either, or subsequent tests will be invalidated.
This should be fairly easy since there's not _much_ that can be done to "modify" a running Telepresence process.
One very obvious example, though, is that the process can be killed.
Don't do that.

##### End-to-end Debugging

When such a test fails,
the *default* is for it to present a low-information failure in the test suite result.
This may be the test suite hanging and being killed by a pytest timeout.
Or it may be Telepresence crashing and the full Telepresence log being dumped.
These kind of test failures are challenging to debug.
Be sure to examine all of the information available.
If not enough information is available, add logging to Telepresence or the test suite.
*Do* write your tests first, observe them fail, and improve their failure behavior before making them pass.

##### Unit Tests

End-to-end tests provide a highly realistic model of real-world Telepresence behavior.
However, they are not the only option and not always the best option.
For subtle logic (particularly involving many possible outcomes), unit tests may provide a lower-cost option.
A single end-to-end test to verify a gross code path combined with many unit tests to exercise all of the subtleties can provide the best of both worlds.

### Coding standard

Formatting is enforced by the installed `yapf` tool; to reformat the code, you can do:

```console
$ make format
```

### Releasing Telepresence

#### Overview

Every commit to the master branch results in CI building a set of deployable artifacts: Docker images, Linux packages, a JSON blob for [Scout](https://www.telepresence.io/reference/usage_reporting), and a markdown blob for announcing a release on Slack et al.
The artifacts are available for download as a tarball `telepresence-dist.tbz` from the CircleCI artifacts tab on the `deploy` job page.
The release process pushes a set of those artifacts into production.

At the moment, the Linux packages are not tested, other than a minor smoke test. Package repositories are [not tested](https://github.com/telepresenceio/telepresence/issues/109) at all.

#### Theory of operation

0. Recreate your Python virtual environment from scratch and re-run the linters.
   This avoids the frustration of having your release fail in the lint stage in CI, which rebuilds its virtualenv every time.  
   `rm -r virtualenv && make lint`
1. Make sure `docs/reference/changelog.md` has changelog entries for the next release, and today's release date.
   If changelog entries are in the `newsfragments` directory, use [towncrier](https://pypi.org/project/towncrier/) to construct the changelog update.
   towncrier's version management is incompatible with the rest of the universe; specify the new version explicitly.
   Make sure to commit your changes.  
   `virtualenv/bin/towncrier --version 0.xx`  
   `# Edit the change log`  
   `git add docs/reference/changelog.md`  
   `git commit -m "Prep changelog for release 0.xx"`
2. Mark the new version number for Telepresence by tagging in Git.  
   `git tag -a 0.xx -m "Release 0.xx"`
3. Push the new commit and tag to GitHub.  
   `git push origin master --tags`
4. Wait for [CircleCI](https://circleci.com/gh/telepresenceio/workflows/telepresence/tree/master) to finish.
   Make sure all the test pass.
5. Download the tarball of deployable artifacts and unarchive into container in your project directory. It will populate the `dist` subdirectory.  
   `curl -s https://.../telepresence-dist.tbz | tar xjf -`
6. Set up release credentials in the environment:  
   `. /keybase/team/datawireio/tel-release-secrets.sh`
   * `HOMEBREW_KEY` to push to GitHub
   * `PACKAGECLOUD_TOKEN` to push Linux packages
   * `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY` for AWS S3 access
7. Run the release script.  
   `ci/release.sh`
8. Log in to [Netlify](https://app.netlify.com/teams/telepresence/sites) and rebuild the Telepresence website.
   Select "Deploys" then "Trigger Deploy" then "Clear cache and deploy site" to get a clean build.
   Without this step, more often than not, the website will display an old version number.
9. Post the release announcement on Slack et al.
   The release script outputs the announcement, or you can find it in `dist/announcement.md`.

#### What the release script does

1. Build and launch a Docker container with required tools.
2. Upload Linux packages to PackageCloud using the script that's generated by the deployment script.
3. Update the Homebrew formula in [homebrew-blackbird](https://github.com/datawire/homebrew-blackbird).
   The Homebrew formula refers to the tarball GitHub [generates for each release](https://github.com/telepresenceio/telepresence/releases).
4. Push the Scout blobs to the `datawire-static-files` S3 bucket.
   [In the future](https://github.com/telepresenceio/telepresence/issues/285), Telepresence will be able to inform users that a new version is available using this data.


### Running tests

#### Full test suites

In order to run *all* possible code paths in Telepresence, you need to do the following:

| Test environment   | How to run                                           |
|--------------------|------------------------------------------------------|
| Minikube           | `make minikube-test`                                 |
| Remote K8s cluster | Runs on Circle                                       |
| Minishift          | `make openshift-tests` with minishift kube context   |
| Remote OS cluster  | `make openshift-tests` with remote OpenShift context |
| Docker on Mac      | `make minikube-test` on Mac with Docker              |
| Other Mac          | Runs on Circle                                       |

In practice running on remote OpenShift cluster usually doesn't happen.

CircleCI on Mac does not yet support Docker, which is why that needs to be done manually.

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
