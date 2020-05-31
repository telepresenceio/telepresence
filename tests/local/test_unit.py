# Copyright 2018 Datawire. All rights reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
"""
Unit tests (in-memory, small units of code).
"""

import ipaddress
import subprocess
import sys
import tempfile

import pytest
import yaml
from hypothesis import example, given
from hypothesis import strategies as st

import telepresence.cli
import telepresence.main
import telepresence.outbound.container
import telepresence.outbound.cidr
import telepresence.outbound.vpn
import telepresence.proxy.deployment
import telepresence.runner.output
from telepresence.runner.cache import Cache
from telepresence.runner.kube import KubeInfo
from telepresence.runner.runner import Runner

COMPLEX_DEPLOYMENT = """\
apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-nginx
spec:
  selector:
    matchLabels:
      name: my-nginx
  replicas: 3
  template:
    metadata:
      labels:
        app: nginx
    spec:
      volumes:
      - name: secret-volume
        secret:
          secretName: nginxsecret
      - name: configmap-volume
        configMap:
          name: nginxconfigmap
      containers:
      - name: dontchange
        image: ymqytw/nginxhttps:1.5
        command: ["/home/auto-reload-nginx.sh"]
        args: ["lalalal"]
        terminationMessagePolicy: "File"
        workingDir: "/somewhere/over/the/rainbow"
        ports:
        - containerPort: 443
        - containerPort: 80
        livenessProbe:
          httpGet:
            path: /index.html
            port: 80
          initialDelaySeconds: 30
          timeoutSeconds: 1
        readinessProbe:
          httpGet:
            path: /index.html
            port: 80
          initialDelaySeconds: 30
          timeoutSeconds: 1
        volumeMounts:
        - mountPath: /etc/nginx/ssl
          name: secret-volume
        - mountPath: /etc/nginx/conf.d
          name: configmap-volume
      - name: nginxhttps
        image: ymqytw/nginxhttps:1.5
        command: ["/home/auto-reload-nginx.sh"]
        args: ["lalalal"]
        terminationMessagePolicy: "File"
        workingDir: "/somewhere/over/the/rainbow"
        imagePullPolicy: Latest
        ports:
        - containerPort: 80
          name: http-api
          protocol: TCP
        livenessProbe:
          httpGet:
            path: /index.html
            port: 80
          initialDelaySeconds: 30
          timeoutSeconds: 1
        readinessProbe:
          httpGet:
            path: /index.html
            port: 80
          initialDelaySeconds: 30
          timeoutSeconds: 1
        volumeMounts:
        - mountPath: /etc/nginx/ssl
          name: secret-volume
        - mountPath: /etc/nginx/conf.d
          name: configmap-volume
        lifecycle:
          postStart:
            exec:
              command: ["/bin/sh", "-c", "echo postStart handler"]
          preStop:
            exec:
              command: ["/usr/sbin/nginx","-s","quit"]
"""

SWAPPED_DEPLOYMENT = """\
apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-nginx
  labels:
    telepresence: random_id_123
spec:
  selector:
    matchLabels:
      name: my-nginx
  replicas: 1
  template:
    metadata:
      labels:
        app: nginx
        telepresence: random_id_123
    spec:
      volumes:
      - name: secret-volume
        secret:
          secretName: nginxsecret
      - name: configmap-volume
        configMap:
          name: nginxconfigmap
      containers:
      - name: dontchange
        image: ymqytw/nginxhttps:1.5
        command: ["/home/auto-reload-nginx.sh"]
        args: ["lalalal"]
        terminationMessagePolicy: "File"
        workingDir: "/somewhere/over/the/rainbow"
        ports:
        - containerPort: 443
        - containerPort: 80
        livenessProbe:
          httpGet:
            path: /index.html
            port: 80
          initialDelaySeconds: 30
          timeoutSeconds: 1
        readinessProbe:
          httpGet:
            path: /index.html
            port: 80
          initialDelaySeconds: 30
          timeoutSeconds: 1
        volumeMounts:
        - mountPath: /etc/nginx/ssl
          name: secret-volume
        - mountPath: /etc/nginx/conf.d
          name: configmap-volume
      - name: nginxhttps
        image: ___replace___me___
        command: ["/usr/src/app/run.sh"]
        terminationMessagePolicy: "FallbackToLogsOnError"
        imagePullPolicy: "IfNotPresent"
        ports:
        - containerPort: 80
          name: http-api
          protocol: TCP
        volumeMounts:
        - mountPath: /etc/nginx/ssl
          name: secret-volume
        - mountPath: /etc/nginx/conf.d
          name: configmap-volume
        env:
        - name: TELEPRESENCE_CONTAINER_NAMESPACE
          valueFrom:
            fieldRef:
              fieldPath: metadata.namespace
"""


def test_swap_deployment_changes():
    """
    The modified Deployment used to swap out an existing Deployment replaces
    all values that might break our own image.
    """
    runner = Runner("-", False)
    runner.kubectl = KubeInfo(
        "cluster",
        "cluster_version",
        False,  # cluster_is_openshift
        "kubectl",
        "command_version",
        "server",
        "context",
        "namespace",
        False,  # in_local_vm
        False,  # verbose
    )

    # Original has a privileged port
    original = yaml.safe_load(COMPLEX_DEPLOYMENT)
    expected = yaml.safe_load(SWAPPED_DEPLOYMENT)
    ports = telepresence.cli.PortMapping.parse(["9999"])
    actual = telepresence.proxy.deployment.new_swapped_deployment(
        runner, original, "nginxhttps", "random_id_123", ports, "", False
    )
    image = actual["spec"]["template"]["spec"]["containers"][1]["image"]
    assert "/telepresence-k8s-priv:" in image
    expected["spec"]["template"]["spec"]["containers"][1]["image"] = image
    assert actual == expected
    assert (9999, 9999) in ports.local_to_remote()
    assert (80, 80) in ports.local_to_remote()

    # Test w/o privileged port.
    original = yaml.safe_load(COMPLEX_DEPLOYMENT)
    original["spec"]["template"]["spec"]["containers"][1]["ports"][0][
        "containerPort"] = 8080
    expected = yaml.safe_load(SWAPPED_DEPLOYMENT)
    expected["spec"]["template"]["spec"]["containers"][1]["ports"][0][
        "containerPort"] = 8080
    ports = telepresence.cli.PortMapping.parse(["9999"])
    actual = telepresence.proxy.deployment.new_swapped_deployment(
        runner, original, "nginxhttps", "random_id_123", ports, "", False
    )
    image = actual["spec"]["template"]["spec"]["containers"][1]["image"]
    assert "/telepresence-k8s:" in image
    expected["spec"]["template"]["spec"]["containers"][1]["image"] = image
    assert actual == expected
    assert (9999, 9999) in ports.local_to_remote()
    assert (8080, 8080) in ports.local_to_remote()

    # Test with OpenShift.
    runner.kubectl = runner.kubectl._replace(
        command="oc",
        cluster_is_openshift=True,
    )
    original = yaml.safe_load(COMPLEX_DEPLOYMENT)
    original["spec"]["template"]["spec"]["containers"][1]["ports"][0][
        "containerPort"] = 8080
    expected = yaml.safe_load(SWAPPED_DEPLOYMENT)
    expected["spec"]["template"]["spec"]["containers"][1]["ports"][0][
        "containerPort"] = 8080
    ports = telepresence.cli.PortMapping.parse(["9999"])
    actual = telepresence.proxy.deployment.new_swapped_deployment(
        runner, original, "nginxhttps", "random_id_123", ports, "", False
    )
    image = actual["spec"]["template"]["spec"]["containers"][1]["image"]
    assert "/telepresence-ocp:" in image
    expected["spec"]["template"]["spec"]["containers"][1]["image"] = image
    assert actual == expected
    assert (9999, 9999) in ports.local_to_remote()
    assert (8080, 8080) in ports.local_to_remote()


def test_portmapping():
    """
    Manually set exposed ports always override automatically exposed ports.
    """
    ports = telepresence.cli.PortMapping.parse(["1234:80", "90"])
    ports.merge_automatic_ports([80, 555, 666])
    assert ports.local_to_remote() == {(1234, 80), (90, 90), (555, 555),
                                       (666, 666)}
    assert ports.remote() == {80, 90, 555, 666}


# Generate a random IPv4 as a string:
ip = st.integers(min_value=0, max_value=2**32 -
                 1).map(lambda i: str(ipaddress.IPv4Address(i)))
# Generate a list of IPv4 strings:
ips = st.lists(elements=ip, min_size=1)


@given(ips)
@example(["1.2.3.4", "1.2.3.5"])
@example(["0.0.0.1", "255.255.255.255"])
def test_covering_cidr(ips):
    """
    covering_cidr() gets the minimal CIDR that covers given IPs.

    In particular, that means any subnets should *not* cover all given IPs.
    """
    cidr = telepresence.outbound.cidr.covering_cidr(ips)
    assert isinstance(cidr, str)
    cidr = ipaddress.IPv4Network(cidr)
    assert cidr.prefixlen <= 24
    # All IPs in given CIDR:
    ips = [ipaddress.IPv4Address(i) for i in ips]
    assert all([ip in cidr for ip in ips])
    # Subnets do not contain all IPs if we're not in minimum 24 bit CIDR:
    if cidr.prefixlen < 24:
        for subnet in cidr.subnets():
            assert not all([ip in subnet for ip in ips])


def test_output_file():
    """Test some reasonable values for the log file"""
    # stdout
    lf_dash = telepresence.runner.output.Output("-")
    assert lf_dash.logfile is sys.stdout, lf_dash.logfile
    # /dev/null -- just make sure we don't crash
    telepresence.runner.output.Output("/dev/null")
    # Regular file -- make sure the file has been truncated
    o_content = "original content\n"
    with tempfile.NamedTemporaryFile(mode="w", delete=False) as out:
        out.write(o_content + "This should be truncated away.\nThis too.\n")
    lf_file = telepresence.runner.output.Output(out.name)
    n_content = "replacement content\n"
    lf_file.write(n_content)
    with open(out.name) as in_again:
        read_content = in_again.read()
        assert o_content not in read_content, read_content
        assert n_content in read_content, read_content


def test_docker_publish_args():
    """Test extraction of docker publish arguments"""
    parse_docker_args = telepresence.outbound.container.parse_docker_args

    expected_docker = ['--rm', '-it', 'fedora:latest', 'curl', 'qotm']
    expected_publish = ['-p=8000:localhost:8000']

    no_publish = "--rm -it fedora:latest curl qotm".split()
    assert parse_docker_args(no_publish) == (expected_docker, [])
    publish_variants = [
        "--rm -it -p 8000:localhost:8000 fedora:latest curl qotm",
        "--rm -it --publish 8000:localhost:8000 fedora:latest curl qotm",
        "--rm -it -p=8000:localhost:8000 fedora:latest curl qotm",
        "--rm -it --publish=8000:localhost:8000 fedora:latest curl qotm",
    ]
    for variant in publish_variants:
        assert parse_docker_args(variant.split()) == \
               (expected_docker, expected_publish)


def test_default_method():
    """
    The ``--method`` argument is optional and defaults to *vpn-tcp*.
    """
    args = telepresence.cli.parse_args([])
    assert args.method == "vpn-tcp"


def test_docker_run_implies_container_method():
    """
    If a value is given for the ``--docker-run`` argument then the method is
    *container*.
    """
    args = telepresence.cli.parse_args([
        "--docker-run", "foo:latest", "/bin/bash"
    ])
    assert args.method == "container"


def test_default_operation():
    """
    The default operation is ``--new-deployment``.
    """
    args = telepresence.cli.parse_args([])
    assert args.new_deployment is not None
    assert args.deployment is None
    assert args.swap_deployment is None


def test_cache():
    cache = Cache({})
    assert "foo" not in cache
    assert cache.lookup("foo", lambda: 3) == 3
    assert "foo" in cache


def test_cache_invalidation():
    cache = Cache({})
    cache.invalidate(12 * 60 * 60)

    cache["pi"] = 3
    cache.invalidate(12 * 60 * 60)
    assert "pi" in cache
    cache.invalidate(-1)
    assert "pi" not in cache


def test_get_output():
    count = 11**5
    cmd = ["python3", "-c", "for idx in range({}): print(idx)".format(count)]

    # Test verbose == False
    runner = Runner("/dev/null", False)
    data = runner.get_output(cmd)
    lines = data.splitlines()
    assert len(lines) == count

    # Test verbose == True
    runner = Runner("/dev/null", True)
    data = runner.get_output(cmd)
    lines = data.splitlines()
    assert len(lines) == count


def test_check_call_timeout():
    code = "import time\nfor n in range(100):\n print(n)\n time.sleep(0.05)"
    cmd = ["python3", "-c", code]

    # Test verbose == False
    runner = Runner("-", False)
    with pytest.raises(subprocess.TimeoutExpired):  # as exc_info
        runner.check_call(cmd, timeout=0.5)
    # FIXME output capture is broken. Everything appears at the end, which
    # means in the timeout case, no output is ever captured.
    '''
    output = exc_info.value.output
    assert "0" in output  # FIXME
    assert "1" in output
    '''

    # Test verbose == True
    runner = Runner("-", True)
    with pytest.raises(subprocess.TimeoutExpired):  # as exc_info
        runner.check_call(cmd, timeout=0.5)
    # FIXME output capture is broken. Everything appears at the end, which
    # means in the timeout case, no output is ever captured.
    '''
    output = exc_info.value.output
    assert "0" in output  # FIXME
    assert "1" in output
    '''
