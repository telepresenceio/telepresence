import os
from functools import partial
from json import JSONDecodeError, dumps, loads
from pathlib import Path
from random import randrange, shuffle
from shutil import which
from socket import AF_INET, SOCK_STREAM, getaddrinfo, gethostbyaddr
from struct import unpack
from subprocess import (
    PIPE, STDOUT, CalledProcessError, Popen, TimeoutExpired, check_call,
    check_output
)
from sys import executable, stdout
from time import sleep

from telepresence.utilities import find_free_port

from .utils import (
    DIRECTORY, KUBECTL, cleanup_namespace, create_namespace, random_name,
    run_webserver, telepresence_image_version
)

REGISTRY = os.environ.get("TELEPRESENCE_REGISTRY", "datawire")
ENVFILE_PATH = Path("/tmp")

LOCAL_WEB_PORT = 12399
LOCAL_WEB_CONTAINER_PORT = 8000


def retry(condition, function):
    while True:
        result = function()
        if not condition(result):
            return result


class _RandomPortAssigner(object):
    """
    Provide ports in the requested range in an unstable order and
    without replacement.  This reduces the chances that concurrent runs
    of the test suite will try to use the same port number.
    """
    def __init__(self, low, high):
        self.low = low
        self.high = high

    def __iter__(self):
        ports = list(range(self.low, self.high))
        shuffle(ports)
        return iter(ports)


_random_ports = iter(_RandomPortAssigner(20000, 40000))


def random_port():
    """
    :return int: A port number which is unique within the scope of this
        process.
    """
    return next(_random_ports)


class HTTPServer(object):
    def __init__(self, local_port, remote_port, value):
        self.local_port = local_port
        if remote_port is None:
            remote_port = local_port
        self.remote_port = remote_port
        self.value = value

    def expose_string(self):
        if self.local_port == self.remote_port:
            return str(self.local_port)
        return "{}:{}".format(self.local_port, self.remote_port)


class _ContainerMethod(object):
    name = "container"

    def unsupported(self):
        missing = set()
        for exe in {"docker"}:
            if which(exe) is None:
                missing.add(exe)
        if missing:
            return "Required executables {} not found on $PATH".format(
                missing,
            )
        return None

    def command_has_graceful_failure(self, command):
        return False

    def loopback_is_host(self):
        return False

    def inherits_client_environment(self):
        return False

    def telepresence_args(self, probe):
        return [
            "--method",
            "container",
            "--container-to-host",
            "{}:{}".format(LOCAL_WEB_CONTAINER_PORT, LOCAL_WEB_PORT),
            "--docker-run",
            # The probe wants to use stdio to communicate with the test process
            "--interactive",
            # Put the probe into the container filesystem.
            "--volume",
            "{}:/probe.py".format(probe),
            # More or less any image that can run a Python 3 program is fine.
            "python:3-alpine",
            "python",
            "/probe.py",
        ]


class _InjectTCPMethod(object):
    name = "inject-tcp"

    def unsupported(self):
        return None

    def command_has_graceful_failure(self, command):
        return command in {
            "ping",
            "traceroute",
            "nslookup",
            "host",
            "dig",
        }

    def loopback_is_host(self):
        return True

    def inherits_client_environment(self):
        return True

    def telepresence_args(self, probe):
        return [
            "--method",
            "inject-tcp",
            "--run",
            executable,
            probe,
        ]


class _VPNTCPMethod(object):
    name = "vpn-tcp"

    def unsupported(self):
        return None

    def command_has_graceful_failure(self, command):
        return command in {
            "ping",
            "traceroute",
        }

    def loopback_is_host(self):
        return True

    def inherits_client_environment(self):
        return True

    def telepresence_args(self, probe):
        return [
            "--method",
            "vpn-tcp",
            "--run",
            executable,
            probe,
        ]


class _TeleproxyMethod(object):
    name = "teleproxy"

    def unsupported(self):
        missing = set()
        for exe in {"edgectl"}:
            if which(exe) is None:
                missing.add(exe)
        if missing:
            return "Required executables {} not found on $PATH".format(
                missing,
            )
        return None

    def command_has_graceful_failure(self, command):
        return command in {
            "ping",
            "traceroute",
        }

    def loopback_is_host(self):
        return True

    def inherits_client_environment(self):
        return True

    def telepresence_args(self, probe):
        return [
            "--method",
            "teleproxy",
            "--run",
            executable,
            probe,
        ]


class _ExistingDeploymentOperation(object):
    def __init__(self, swap):
        self.swap = swap
        self.json_env = None  # Filled in below
        self.envfile = None  # Filled in below
        if swap:
            self.name = "swap"
            self.image = "openshift/hello-openshift"
            self.replicas = 2
            # An argument list to use to override the default command of the
            # container. This allows the swap-deployment tests to verify that a
            # command is restored after Telepresence swaps the original
            # deployment back in.
            self.container_args = ["/hello-openshift"]
            self.http_server_auto_expose_same = HTTPServer(
                random_port(),
                None,
                random_name("auto-same"),
            )
            print(
                "HTTP Server auto-expose same-port: {}".format(
                    self.http_server_auto_expose_same.remote_port,
                )
            )
            self.http_server_auto_expose_diff = HTTPServer(
                12330,
                random_port(),
                random_name("auto-diff"),
            )
            print(
                "HTTP Server auto-expose diff-port: {}".format(
                    self.http_server_auto_expose_diff.remote_port,
                )
            )
        else:
            self.name = "existing"
            self.image = "{}/telepresence-k8s:{}".format(
                REGISTRY,
                telepresence_image_version(),
            )
            self.replicas = 1
            self.container_args = None

    def inherits_deployment_environment(self):
        return True

    def prepare_deployment(self, deployment_ident, environ):
        if self.swap:
            ports = [
                {
                    "containerPort": self.http_server_auto_expose_same.
                    local_port,
                    "hostPort": self.http_server_auto_expose_same.remote_port,
                },
                {
                    "containerPort": self.http_server_auto_expose_diff.
                    local_port,
                    "hostPort": self.http_server_auto_expose_diff.remote_port,
                },
            ]
        else:
            ports = []

        create_deployment(
            deployment_ident,
            self.image,
            self.container_args,
            environ,
            ports,
            replicas=self.replicas,
        )

        self.json_env = ENVFILE_PATH / (deployment_ident.name + ".json")
        self.envfile = ENVFILE_PATH / (deployment_ident.name + ".env")

    def cleanup_deployment(self, deployment_ident):
        _cleanup_deployment(deployment_ident)
        if self.json_env:
            self.json_env.unlink()
        if self.envfile:
            self.envfile.unlink()

    def auto_http_servers(self):
        if self.swap:
            return [
                self.http_server_auto_expose_same,
                self.http_server_auto_expose_diff,
            ]
        return []

    def prepare_service(self, deployment_ident, ports):
        create_service(deployment_ident, ports)

    def cleanup_service(self, deployment_ident):
        cleanup_service(deployment_ident)

    def telepresence_args(self, deployment_ident):
        if self.swap:
            option = "--swap-deployment"
        else:
            option = "--deployment"
        return [
            "--namespace",
            deployment_ident.namespace,
            option,
            deployment_ident.name,
            "--to-pod",
            "8910",
            "--from-pod",
            "9876",
            "--env-json",
            str(self.json_env),
            "--env-file",
            str(self.envfile),
        ]


class _NewDeploymentOperation(object):
    name = "new"

    def inherits_deployment_environment(self):
        return False

    def prepare_deployment(self, deployment_ident, environ):
        pass

    def cleanup_deployment(self, deployment_ident):
        pass

    def auto_http_servers(self):
        return []

    def prepare_service(self, deployment_ident, ports):
        pass

    def cleanup_service(self, deployment_ident):
        pass

    def telepresence_args(self, deployment_ident):
        return [
            "--namespace",
            deployment_ident.namespace,
            "--new-deployment",
            deployment_ident.name,
        ]


def create_deployment(deployment_ident, image, args, environ, ports, replicas):
    """
    Create a ``Deployment`` in the current context.

    :param ResourceIdent deployment_ident: The identifier to assign to the
        deployment.

    :param str image: The Docker image to put in the Deployment's pod
        template.

    :param list[str] args: An argument list to specify as the command for the
        image.  Or ``None`` to use the image default.

    :param dict[str, str] environ: The environment to put in the Deployment's
        pod template.

    :param int replicas: The number of replicas to configure for the
        Deployment.

    :raise CalledProcessError: If the *kubectl* command returns an error code.
    """
    container = {
        "name": "hello",
        "image": image,
        "env": list({
            "name": k,
            "value": v
        } for (k, v) in environ.items()),
        "volumeMounts": [{
            "name": "podinfo",
            "mountPath": "/podinfo",
        }],
        "securityContext": {
            "readOnlyRootFilesystem": True
        }
    }
    if args is not None:
        container["args"] = args
    if ports is not None:
        container["ports"] = ports

    sidecar_container = {
        "name": "sidecar",
        "image": "datawire/tel-sidecar-test-helper:1",
    }

    deployment = dumps({
        "kind": "Deployment",
        "apiVersion": "apps/v1",
        "metadata": {
            "name": deployment_ident.name,
            "namespace": deployment_ident.namespace,
        },
        "spec": {
            "selector": {
                "matchLabels": {
                    "name": deployment_ident.name
                }
            },
            "replicas": replicas,
            "template": {
                "metadata": {
                    "labels": {
                        "name": deployment_ident.name,
                        "telepresence-test": deployment_ident.name,
                        "hello": "monkeys",
                    },
                },
                "spec": {
                    "volumes": [{
                        "name": "podinfo",
                        "downwardAPI": {
                            "items": [{
                                "path": "labels",
                                "fieldRef": {
                                    "fieldPath": "metadata.labels"
                                },
                            }],
                        },
                    }],
                    "containers": [container, sidecar_container],
                },
            },
        },
    })
    check_output([KUBECTL, "create", "-f", "-"],
                 input=deployment.encode("utf-8"))


def create_service(deployment_ident, ports):
    if not ports:
        return
    service_obj = {
        "kind": "Service",
        "apiVersion": "v1",
        "metadata": {
            "name": deployment_ident.name,
            "namespace": deployment_ident.namespace,
        },
        "spec": {
            "selector": {
                "telepresence-test": deployment_ident.name
            },
            "type": "ClusterIP",
            "ports": []
        }
    }
    for port in ports:
        service_obj["spec"]["ports"].append({
            "name": "expose-port-{}".format(port),
            "protocol": "TCP",
            "port": port,
            "targetPort": port
        })
    service = dumps(service_obj)
    check_output([KUBECTL, "create", "-f", "-"], input=service.encode("utf-8"))


def cleanup_service(deployment_ident):
    check_call([
        KUBECTL, "delete", "--namespace", deployment_ident.namespace,
        "--ignore-not-found", "service", deployment_ident.name, "--wait=false"
    ])


INJECT_TCP_METHOD = _InjectTCPMethod()
NEW_DEPLOYMENT_OPERATION = _NewDeploymentOperation()

METHODS = [
    _ContainerMethod(),
    _VPNTCPMethod(),
    INJECT_TCP_METHOD,
    _TeleproxyMethod(),
]
OPERATIONS = [
    _ExistingDeploymentOperation(False),
    _ExistingDeploymentOperation(True),
    NEW_DEPLOYMENT_OPERATION,
]


class ResourceIdent(object):
    """
    Identify a Kubernetes resource.
    """
    def __init__(self, namespace, name):
        self.namespace = namespace
        self.name = name


def _cleanup_deployment(ident):
    check_call([
        KUBECTL,
        "delete",
        "--namespace",
        ident.namespace,
        "--ignore-not-found",
        "deployment",
        ident.name,
        "--wait=false",
    ])


def _telepresence(telepresence_args, env=None):
    """
    Run a probe in a Telepresence execution context.

    :param list telepresence: Arguments to pass to the Telepresence CLI.

    :param env: Environment variables to set for the Telepresence CLI.  These
        are added to the current process's environment.  ``None`` means the
        same thing as ``{}``.

    :return Popen: A ``Popen`` object corresponding to the running
        Telepresence process.
    """
    args = [
        executable,
        which("telepresence"),
        "--logfile=-",
    ] + telepresence_args

    pass_env = os.environ.copy()
    if env is not None:
        pass_env.update(env)

    print("Running {}".format(args))
    return Popen(
        args=args,
        stdin=PIPE,
        stdout=PIPE,
        stderr=STDOUT,
        bufsize=0,
        env=pass_env,
    )


def run_telepresence_probe(
    request,
    method,
    operation,
    desired_environment,
    client_environment,
    probe_urls,
    probe_commands,
    probe_paths,
    also_proxy,
    http_servers,
    desired_exit_code,
):
    """
    :param request: The pytest mumble mumble whatever.

    :param method: The definition of a Telepresence method to use for this
        run.

    :param operation: The definition of a Telepresence operation to use
        for this run.

    :param dict desired_environment: Key/value pairs to set in the probe's
        environment.

    :param dict client_environment: Key/value pairs to set in the Telepresence
        CLI's environment.

    :param list[str] probe_urls: URLs to direct the probe process to request
        and return to us.

    :param list[str] probe_commands: Commands (argv[0]) to direct the probe to
        attempt to run and report back on the results.

    :param list[str] probe_paths: Paths relative to $TELEPRESENCE_ROOT to
        direct the probe to read and report back to us.

    :param list[AlsoProxy] also_proxy: Values to pass to Telepresence as
        ``--also-proxy`` arguments.

    :param list[HTTPServer] http_servers: Configuration for HTTP servers to
        pass to the Telepresence probe with `--http-...``.

    :param int desired_exit_code: The probe's exit status.
    """
    probe_endtoend = (Path(__file__).parent / "probe_endtoend.py").as_posix()

    # Create a web server service.  We'll observe side-effects related to
    # this, such as things set in our environment, and also interact with
    # it directly to demonstrate behaviors related to networking.  It's
    # important that we create this before the ``prepare_deployment`` step
    # below because the environment supplied by Kubernetes to a
    # Deployment's containers depends on the state of the cluster at the
    # time of pod creation.
    deployment_ident = ResourceIdent(
        namespace=random_name("ns"),
        name=random_name("test"),
    )
    # Create the Kubernetes Namespace everything related to this run will live
    # in.  Cleanup is the responsibility of the Probe we return.
    create_namespace(deployment_ident.namespace, deployment_ident.name)

    # TODO: Factor run_webserver into a fixture that Probe can manage so that
    # run_telepresence_probe can just focus on running telepresence.

    # This is an extra pod running on Kubernetes so that various tests can
    # observe how such a thing impacts on the Telepresence execution
    # environment (e.g., environment variables set, etc).
    webserver_name = run_webserver(deployment_ident.namespace)

    operation.prepare_deployment(
        deployment_ident,
        desired_environment,
    )
    print(
        "Prepared deployment {}/{}".format(
            deployment_ident.namespace,
            deployment_ident.name,
        )
    )

    # Make sure we expose every port with an http server that the tests want
    # to talk to.
    service_ports = [http.remote_port for http in http_servers]

    # Also give the operation a chance to declare additional ports.  This is
    # used by the auto-expose swap-deployment tests to make sure that ports on
    # an existing Deployment are still exposed even if the Telepresence
    # command line doesn't have the arguments to expose them.
    auto_http_servers = operation.auto_http_servers()
    service_ports.extend([http.remote_port for http in auto_http_servers])

    # Tell the operation to prepare a service exposing those ports.
    print("Creating service with ports {}".format(service_ports))
    operation.prepare_service(deployment_ident, service_ports)

    probe_args = []
    for url in probe_urls:
        probe_args.extend(["--probe-url", url])
    for command in probe_commands:
        probe_args.extend(["--probe-command", command])
    for path in probe_paths:
        probe_args.extend(["--probe-path", path])
    for http in http_servers + auto_http_servers:
        probe_args.extend([
            "--http-port",
            str(http.local_port),
            "--http-value",
            http.value,
        ])
    probe_args.extend(["--exit-code", str(desired_exit_code)])

    telepresence_args = []
    # FIXME: Teleproxy method rejects --also-proxy
    if method.name == "teleproxy":
        also_proxy = []
    for addr in also_proxy:
        telepresence_args.extend(["--also-proxy", addr])
    for http in http_servers:
        telepresence_args.extend([
            "--expose",
            http.expose_string(),
        ])

    operation_args = operation.telepresence_args(deployment_ident)
    method_args = method.telepresence_args(probe_endtoend)
    args = operation_args + telepresence_args + method_args + probe_args
    try:
        telepresence = _telepresence(args, client_environment)
    except CalledProcessError as e:
        assert False, "Failure running {}: {}\n{}".format(
            ["telepresence"] + args,
            str(e),
            e.output.decode("utf-8"),
        )
    else:
        writer = stdout.buffer
        output = _read_tagged_output(
            telepresence,
            telepresence.stdout,
            writer,
        ).decode("utf-8")
        try:
            initial_result = loads(output)
        except JSONDecodeError as e:
            assert False, \
                "Could not decode JSON probe result from {}:\n{}".format(
                    ["telepresence"] + args, e.doc
                )
        return ProbeResult(
            writer,
            telepresence,
            deployment_ident,
            webserver_name,
            initial_result,
        )


class NoTaggedValue(Exception):
    """
    Attempted to read a tagged value from the Telepresence process but all
    Telepresence output was examined (Telepresence has exited) and there was
    none.
    """


# See probe_endtoend.py
MAGIC_PREFIX = b"\xc0\xc1\xfe\xff"


def _read_tagged_output(process, output, writer):
    """
    Read some structured data from the ``output`` stream of the
    Telepresence/probe process.  Write any unstructured data found on the way
    to ``writer``.
    """
    data = b""
    length = None
    while True:
        returncode = process.poll()
        if returncode is None:
            # The process hasn't exited.  Try to do a partial read of its
            # output.
            new_data = output.read(2**16)
            if not new_data:
                # Don't poll excessively if nothing is coming out.
                sleep(1)
                continue
        else:
            # The process has exited.  Read everything that's left.
            new_data = output.read()

        data += new_data
        tag = data.find(MAGIC_PREFIX)
        if tag == -1:
            # Try to produce output that is streaming to the greatest degree
            # possible.  If the first byte of the tag doesn't even appear in
            # data, we know nothing in data is going to be relevant to our tag
            # search so we can send all of data onwards right now.
            if MAGIC_PREFIX[0] not in data:
                writer.write(data)
                data = b""
        else:
            # Found the tag.  We can send anything before it onwards.
            if tag > 0:
                writer.write(data[:tag])
                data = data[tag:]

            if len(data) >= 8:
                # There's enough data left that the 4 byte length prefix is
                # complete.
                [length] = unpack(">I", data[4:8])

                if len(data) >= length + 8:
                    # There's enough data to satisfy the length prefix.  We
                    # found the tagged output.  Grab it.
                    tagged = data[8:length + 8]
                    remaining = data[length + 8:]

                    # Strange buffering interactions in the way the probe
                    # writes its output means there may actually be data left.
                    # This is unfortunate.  Guess we'll just deal with it,
                    # though.  There _shouldn't_ be any more _tagged_ data.
                    assert MAGIC_PREFIX[0] not in remaining
                    writer.write(remaining)
                    remaining = b""
                    return tagged

        # Process exited, we parsed all of its output, we're done after we
        # pass along any untagged data we have buffered.
        if returncode is not None:
            if data:
                writer.write(data)
                data = b""
            break
    raise NoTaggedValue()


class ProbeResult(object):
    def __init__(
        self, writer, telepresence, deployment_ident, webserver_name, result
    ):
        self._writer = writer
        self.telepresence = telepresence
        self.deployment_ident = deployment_ident
        self.webserver_name = webserver_name
        self.result = result
        self.returncode = None

    def write(self, command):
        if "\n" in command:
            raise ValueError("Cannot send multiline command.")
        self.telepresence.stdin.write((command + "\n").encode("utf-8"))

    def read(self):
        return _read_tagged_output(
            self.telepresence,
            self.telepresence.stdout,
            self._writer,
        ).decode("utf-8")


class AlsoProxy(object):
    """
    Represent parameters of a particular case to test of ``--also-proxy``.
    """
    def __init__(self, argument, host):
        """
        :param str argument: The value to supply to ``--also-proxy``.

        :param str host: The host component of a URL to request to verify the
            feature is working.  This should be an address which gets proxied
            by Telepresence because ``argument`` was passed to
            ``--also-proxy``.
        """
        self.argument = argument
        self.host = host


_json_blob = """{
    "a": "b",
    "c": "d",
    "really long key": "really long value",
    "multiline key": "-----BEGIN PGP PUBLIC KEY BLOCK-----\nmQENBFrVHZEBCACuD163edXBofnt8qNyluDufnIp0PucPZmK0lUuaJT/xi5RRki+\ntakVww0LGwPn6mcTI2Tgb2cEIvwk7yyXC5yPOPdWchUCxhfeadIgytDPOm3g51zG\nh/Ob1VH067nZlL1qJ7We4ZP0NGpT+MSVDYwGFROMoliRLe5bqz3SZgCI+GgXiHDU\nNbvkxAHE6Z5ZxkzAjBnJDmOf9kdnIHZvuBAVylHUorjTLN2jxJOQYFx7nqwbaOsA\n6i/5W4/CYm2NwPb09I4H2Hi8qQQ1PN5WV+Ky3PngE6yZTMRk34b1aV5VLJPf3yoi\nfqaqjX9xIetMvg6DZP0FiPqC66DESaEz1rv5ABEBAAG0EHRlc3RAZXhhbXBsZS5j\nb22JARwEEAECAAYFAlrVHZEACgkQ1A1oULTM9GYCigf9EWVBQwsG6LxmjhZ5bFyx\n8WT3H86tUhMgvmPGZEV/jl7VUG69DcGb2mhevN8F3mM/V+6njREwCmF9qKbY5HJj\npgG46Rsm6UrbZVH23CRnHFsQ7M0inyZ1CrhCyMETZYHpKOs7lIdAHH1q9F8fTIW6\n9KTnSe0LQpiV5tNxg2EJzCNYpvyvTOA0mGbi+XbjQJDGKr1xqfMYBr79Os9N/dGe\ncWrpBoHLAzB07ZC5CxdXo7Z21i+cTlTM/2c4tE9dLth3Yzzw9fqXyRqlrG1K8Bmz\n8LKhIHctewW9a6M7JTp48p7fRZiCir7N9Zj0hq6zry8+FHnkZIvFNOHFcbUrfS2Y\n2g==\n=rNSt\n-----END PGP PUBLIC KEY BLOCK-----",
    "one last key": "this last value"
}"""  # noqa: E501


class Probe(object):
    CLIENT_ENV_VAR = "SHOULD_NOT_BE_SET"

    DESIRED_ENVIRONMENT = {
        "MYENV": "hello",
        "EXAMPLE_ENVFROM": "foobar",
        "EX_MULTI_LINE": (
            "first line = (no newline before, newline after)\n"
            "second line = (newline before and after)\n"
        ),
        "EX_JSON_BLOB_FROM_597": _json_blob,
    }

    # A resource available from a server running on the Telepresence host
    # which the tests can use to verify correct routing-to-host behavior from
    # the Telepresence execution context.
    LOOPBACK_URL_TEMPLATE = "http://localhost:{}/test_endtoend.py"

    # Commands which indirectly interact with Telepresence in some way and
    # which may not be s upported (and which we care about them failing in a
    # nice way).
    QUESTIONABLE_COMMANDS = [
        "ping",
        "traceroute",
        "nslookup",
        "host",
        "dig",
    ]

    # Paths relative to $TELEPRESENCE_ROOT in the Telepresence execution
    # context which the probe will read and return to us.
    INTERESTING_PATHS = [
        "podinfo/labels",
        "var/run/secrets/kubernetes.io/serviceaccount/ca.crt",
    ]

    # Get some httpbin.org addresses.  We avoid the real domain name in the
    # related tests due to
    # <https://github.com/datawire/telepresence/issues/379>.
    _httpbin = iter(
        getaddrinfo(
            "httpbin.org",
            80,
            AF_INET,
            SOCK_STREAM,
        ) * 2
    )

    #
    # Also notice that each ALSO_PROXY_... uses non-overlapping addresses
    # because we run Telepresence once with _all_ of these as ``--also-proxy``
    # arguments.  We want to make sure each case works so we don't want
    # overlapping addresses where an argument of form might work and cause it
    # to appear as though the other cases are also working.  Instead, with a
    # different address each time, each form must be working.
    _an_ip = next(_httpbin)[4][0]
    ALSO_PROXY_HOSTNAME = AlsoProxy(
        # This is just any domain name that resolves to _one_ IP address that
        # will serve up httpbin.org.  See #379.
        gethostbyaddr(_an_ip)[0],
        _an_ip,
    )

    # This time we're exercising Telepresence support for specifying an IP
    # address literal to ``--also-proxy``.
    _an_ip = next(_httpbin)[4][0]
    ALSO_PROXY_IP = AlsoProxy(
        _an_ip,
        _an_ip,
    )

    # This time exercising support for specifying an IP network to
    # ``--also-proxy``.
    _an_ip = next(_httpbin)[4][0]
    ALSO_PROXY_CIDR = AlsoProxy(
        "{}/32".format(_an_ip),
        _an_ip,
    )

    HTTP_SERVER_SAME_PORT = HTTPServer(
        random_port(),
        None,
        random_name("same"),
    )
    print(
        "HTTP Server same-port: {}".format(
            HTTP_SERVER_SAME_PORT.remote_port,
        )
    )
    HTTP_SERVER_DIFFERENT_PORT = HTTPServer(
        12360,
        random_port(),
        random_name("diff"),
    )
    print(
        "HTTP Server diff-port: {}".format(
            HTTP_SERVER_SAME_PORT.remote_port,
        )
    )
    HTTP_SERVER_LOW_PORT = HTTPServer(
        12350,
        # This needs to be allocated from the privileged range.  Try to avoid
        # values that are obviously going to fail.  We only allocate one
        # low-value port number so we don't need special steps to avoid
        # reusing one.
        retry({22, 80, 111, 443}.__contains__, partial(randrange, 1, 1024)),
        random_name("low"),
    )

    _result = None

    def __init__(self, request, method, operation):
        self._request = request
        self.method = method
        self.operation = operation
        self._cleanup = []

    def __str__(self):
        return "Probe[{}, {}]".format(
            self.method.name,
            self.operation.name,
        )

    def result(self):
        if self._result is None:
            print("Launching {}".format(self))

            local_port = find_free_port()
            self.loopback_url = self.LOOPBACK_URL_TEMPLATE.format(local_port)
            # This is a local web server that the Telepresence probe can try to
            # interact with to verify network routing to the host.
            # TODO Just cross our fingers and hope this port is available...
            server_cmd = [executable, "-m", "http.server", str(local_port)]
            p = Popen(server_cmd, cwd=str(DIRECTORY))
            self._cleanup.append(lambda: _cleanup_process(p))

            # This is for testing the container method's connectivity to the
            # host using port forwarding.
            self.fwd_url = self.LOOPBACK_URL_TEMPLATE.format(
                LOCAL_WEB_CONTAINER_PORT
            )
            server_cmd = [executable, "-m", "http.server", str(LOCAL_WEB_PORT)]
            p2 = Popen(server_cmd, cwd=str(DIRECTORY))
            self._cleanup.append(lambda: _cleanup_process(p2))

            also_proxy = [
                self.ALSO_PROXY_HOSTNAME.argument,
                self.ALSO_PROXY_IP.argument,
                self.ALSO_PROXY_CIDR.argument,
            ]
            http_servers = [
                self.HTTP_SERVER_SAME_PORT,
                self.HTTP_SERVER_DIFFERENT_PORT,
                self.HTTP_SERVER_LOW_PORT,
            ]
            self.desired_exit_code = len(str(self))
            self._result = "FAILED"
            self._result = run_telepresence_probe(
                self._request,
                self.method,
                self.operation,
                self.DESIRED_ENVIRONMENT,
                {self.CLIENT_ENV_VAR: "FOO"},
                [self.loopback_url, self.fwd_url],
                self.QUESTIONABLE_COMMANDS,
                self.INTERESTING_PATHS,
                also_proxy,
                http_servers,
                self.desired_exit_code,
            )
            self._cleanup.append(self.ensure_dead)
            self._cleanup.append(self.cleanup_resources)
        assert self._result != "FAILED"
        return self._result

    def cleanup(self):
        print("Cleaning up {}".format(self))
        for cleanup in self._cleanup:
            cleanup()

    def ensure_dead(self):
        """
        Make sure the Telepresence process launched by this Probe is no longer
        running.

        :raise Exception: If no Telepresence process was ever launched by this
            Probe in the first.
        """
        if self._result is None:
            raise Exception("Probe never launched")
        if self._result == "FAILED":
            raise Exception("Probe has failed")
        if self._result.telepresence.returncode is None:
            print("Telling probe {} to quit".format(self))
            self._result.write("done")
            self._result.read()  # Last output should be well-formed
            try:
                self._result.telepresence.wait(timeout=15)
            except TimeoutExpired:
                _cleanup_process(self._result.telepresence)
        self._result.returncode = self._result.telepresence.returncode

    def cleanup_resources(self):
        """
        Delete Kubernetes resources related to this Probe.

        :raise Exception: If no Telepresence process was ever launched by this
            Probe in the first place.
        """
        if self._result is None:
            raise Exception("Probe never launched")

        self.operation.cleanup_deployment(self._result.deployment_ident)
        self.operation.cleanup_service(self._result.deployment_ident)
        cleanup_namespace(self._result.deployment_ident.namespace)


def _cleanup_process(process):
    """
    Terminate and wait on the given process, if it still exists.

    Do nothing if it has already been waited on.
    """
    if process.returncode is None:
        print("Terminating {}".format(process.pid))
        process.terminate()
        print("Waiting on {}".format(process.pid))
        process.wait()
        print("Cleaned up {}".format(process.pid))
