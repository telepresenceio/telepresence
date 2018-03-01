import os
from time import (
    sleep,
)
from struct import (
    unpack,
)
from socket import (
    AF_INET,
    SOCK_STREAM,
    getaddrinfo,
    gethostbyaddr,
)
from sys import (
    executable,
    stdout,
)
from json import (
    JSONDecodeError,
    loads, dumps,
)
from shutil import which
from subprocess import (
    CalledProcessError,
    PIPE, STDOUT,
    Popen,
    check_output, check_call,
)

from pathlib import Path

from telepresence.utilities import find_free_port
from .utils import (
    KUBECTL,
    DIRECTORY,
    random_name,
    run_webserver,
    create_namespace,
    cleanup_namespace,
    telepresence_version,
)

REGISTRY = os.environ.get("TELEPRESENCE_REGISTRY", "datawire")


class _ContainerMethod(object):
    name = "container"

    def unsupported(self):
        missing = set()
        for exe in {"socat", "docker"}:
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
            "--method", "container",
            "--docker-run",
            # The probe wants to use stdio to communicate with the test process.
            "--interactive",
            # Put the probe into the container filesystem.
            "--volume", "{}:/probe.py".format(probe),
            # More or less any image that can run a Python 3 program is fine.
            "python:3-alpine",
            "python", "/probe.py",
        ]


class _InjectTCPMethod(object):
    name = "inject-tcp"

    def unsupported(self):
        return None


    def command_has_graceful_failure(self, command):
        return command in {
            "ping", "traceroute", "nslookup", "host", "dig",
        }


    def loopback_is_host(self):
        return True


    def inherits_client_environment(self):
        return True


    def telepresence_args(self, probe):
        return [
            "--method", "inject-tcp",
            "--run", executable, probe,
        ]



class _VPNTCPMethod(object):
    name = "vpn-tcp"

    def unsupported(self):
        return None


    def command_has_graceful_failure(self, command):
        return command in {
            "ping", "traceroute",
        }


    def loopback_is_host(self):
        return True


    def inherits_client_environment(self):
        return True


    def telepresence_args(self, probe):
        return [
            "--method", "vpn-tcp",
            "--run", executable, probe,
        ]



class _ExistingDeploymentOperation(object):
    def __init__(self, swap):
        self.swap = swap
        if swap:
            self.name = "swap"
        else:
            self.name = "existing"


    def inherits_deployment_environment(self):
        return True


    def prepare_deployment(self, deployment_ident, environ):
        if self.swap:
            image = "openshift/hello-openshift"
        else:
            image = "{}/telepresence-k8s:{}".format(
                REGISTRY,
                telepresence_version(),
            )
        create_deployment(deployment_ident, image, environ)


    def cleanup_deployment(self, deployment_ident):
        _cleanup_deployment(deployment_ident)


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
            "--namespace", deployment_ident.namespace,
            option, deployment_ident.name,
        ]



class _NewDeploymentOperation(object):
    name = "new"

    def inherits_deployment_environment(self):
        return False


    def prepare_deployment(self, deployment_ident, environ):
        pass


    def cleanup_deployment(self, deployment_ident):
        pass


    def prepare_service(self, deployment_ident, ports):
        pass


    def cleanup_service(self, deployment_ident):
        pass


    def telepresence_args(self, deployment_ident):
        return [
            "--namespace", deployment_ident.namespace,
            "--new-deployment", deployment_ident.name,
        ]



def create_deployment(deployment_ident, image, environ):
    deployment = dumps({
        "kind": "Deployment",
        "apiVersion": "extensions/v1beta1",
        "metadata": {
            "name": deployment_ident.name,
            "namespace": deployment_ident.namespace,
        },
        "spec": {
            "replicas": 2,
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
                                "fieldRef": {"fieldPath": "metadata.labels"},
                            }],
                        },
                    }],
                    "containers": [{
                        "name": "hello",
                        "image": image,
                        "env": list(
                            {"name": k, "value": v}
                            for (k, v)
                            in environ.items()
                        ),
                        "volumeMounts": [{
                            "name": "podinfo",
                            "mountPath": "/podinfo",
                        }],
                    }],
                },
            },
        },
    })
    check_output([KUBECTL, "create", "-f", "-"], input=deployment.encode("utf-8"))

def create_service(deployment_ident, ports):
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
        KUBECTL, "delete",
        "--namespace", deployment_ident.namespace,
        "service", deployment_ident.name
    ])


METHODS = [
    _ContainerMethod(),
    _InjectTCPMethod(),
    _VPNTCPMethod(),
]
OPERATIONS = [
    _ExistingDeploymentOperation(False),
    _ExistingDeploymentOperation(True),
    _NewDeploymentOperation(),
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
        KUBECTL, "delete",
        "--namespace", ident.namespace,
        "--ignore-not-found",
        "deployment", ident.name,
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
        pass to the Telepreosence probe with `--http-...``.
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
        namespace=random_name() + "-ns",
        name=random_name() + "-test",
    )
    create_namespace(deployment_ident.namespace, deployment_ident.name)
    request.addfinalizer(lambda: cleanup_namespace(deployment_ident.namespace))

    # TODO: Factor run_webserver into a fixture that Probe can manage so that
    # run_telepresence_probe can just focus on running telepresence.

    # This is an extra pod running on Kubernetes so that various tests can
    # observe how such a thing impacts on the Telepresence execution
    # environment (e.g., environment variables set, etc).
    webserver_name = run_webserver(deployment_ident.namespace)

    operation.prepare_deployment(deployment_ident, desired_environment)
    print("Prepared deployment {}/{}".format(deployment_ident.namespace, deployment_ident.name))
    request.addfinalizer(lambda: operation.cleanup_deployment(deployment_ident))

    operation.prepare_service(
        deployment_ident,
        [http.local_port for http in http_servers]
    )
    request.addfinalizer(lambda: operation.cleanup_service(deployment_ident))

    probe_args = []
    for url in probe_urls:
        probe_args.extend(["--probe-url", url])
    for command in probe_commands:
        probe_args.extend(["--probe-command", command])
    for path in probe_paths:
        probe_args.extend(["--probe-path", path])
    for http in http_servers:
        probe_args.extend([
            "--http-port", str(http.local_port),
            "--http-value", http.value,
        ])

    telepresence_args = []
    for addr in also_proxy:
        telepresence_args.extend(["--also-proxy", addr])
    for http in http_servers:
        telepresence_args.extend([
            "--expose", http.expose_string(),
        ])

    operation_args = operation.telepresence_args(deployment_ident)
    method_args = method.telepresence_args(probe_endtoend)
    args = operation_args + telepresence_args + method_args + probe_args
    try:
        telepresence = _telepresence(args, client_environment)
    except CalledProcessError as e:
        assert False, "Failure running {}: {}\n{}".format(
            ["telepresence"] + args, str(e), e.output.decode("utf-8"),
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
            assert False, "Could not decode JSON probe result from {}:\n{}".format(
                ["telepresence"] + args, e.doc,
            )
        return ProbeResult(
            writer,
            telepresence,
            deployment_ident,
            webserver_name,
            initial_result,
        )



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
            new_data = output.read(2 ** 16)
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
    raise Exception("Failed to find a tagged value.")



class ProbeResult(object):
    def __init__(self, writer, telepresence, deployment_ident, webserver_name, result):
        self._writer = writer
        self.telepresence = telepresence
        self.deployment_ident = deployment_ident
        self.webserver_name = webserver_name
        self.result = result


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



class HTTPServer(object):
    def __init__(self, local_port, remote_port, value):
        self.local_port = local_port
        self.remote_port = remote_port
        self.value = value


    def expose_string(self):
        if self.local_port == self.remote_port:
            return str(self.local_port)
        return "{}:{}".format(self.local_port, self.remote_port)



class Probe(object):
    CLIENT_ENV_VAR = "SHOULD_NOT_BE_SET"

    DESIRED_ENVIRONMENT = {
        "MYENV": "hello",
        "EXAMPLE_ENVFROM": "foobar",

        # XXXX
        # Container method doesn't support multi-line environment variables.
        # Therefore disable this for all methods or the container tests all
        # fail...
        # XXXX
        # "EX_MULTI_LINE": (
        #     "first line (no newline before, newline after)\n"
        #     "second line (newline before and after)\n"
        # ),
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
    _httpbin = iter(getaddrinfo(
        "httpbin.org",
        80,
        AF_INET,
        SOCK_STREAM,
    ))

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

    HTTP_SERVER_SAME_PORT = HTTPServer(12370, 12370, random_name())
    # HTTP_SERVER_DIFFERENT_PORT = (12360, 12355, random_name())
    # HTTP_SERVER_LOW_PORT = (12350, 79, random_name())

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
            self.loopback_url = self.LOOPBACK_URL_TEMPLATE.format(
                local_port,
            )
            # This is a local web server that the Telepresence probe can try to
            # interact with to verify network routing to the host.
            p = Popen(
                # TODO Just cross our fingers and hope this port is available...
                [executable, "-m", "http.server", str(local_port)],
                cwd=str(DIRECTORY),
            )
            self._cleanup.append(lambda: _cleanup_process(p))

            also_proxy = [
                self.ALSO_PROXY_HOSTNAME.argument,
                self.ALSO_PROXY_IP.argument,
                self.ALSO_PROXY_CIDR.argument,
            ]
            http_servers = [
                self.HTTP_SERVER_SAME_PORT,
            ]
            self._result = run_telepresence_probe(
                self._request,
                self.method,
                self.operation,
                self.DESIRED_ENVIRONMENT,
                {self.CLIENT_ENV_VAR: "FOO"},
                [self.loopback_url],
                self.QUESTIONABLE_COMMANDS,
                self.INTERESTING_PATHS,
                also_proxy,
                http_servers
            )
            self._cleanup.append(lambda: _cleanup_process(self._result.telepresence))
        return self._result


    def cleanup(self):
        print("Cleaning up {}".format(self))
        for cleanup in self._cleanup:
            cleanup()



def _cleanup_process(process):
    print("Terminating {}".format(process.pid))
    process.terminate()
    print("Waiting on {}".format(process.pid))
    process.wait()
    print("Cleaned up {}".format(process.pid))
