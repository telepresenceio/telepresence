"""
Unit tests (in-memory, small units of code).
"""

import sys
import tempfile
import ipaddress

from hypothesis import strategies as st, given, example
import yaml

from . import telepresence

COMPLEX_DEPLOYMENT = """\
apiVersion: extensions/v1beta1
kind: Deployment
metadata:
  name: my-nginx
spec:
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
"""

SWAPPED_DEPLOYMENT = """\
apiVersion: extensions/v1beta1
kind: Deployment
metadata:
  name: my-nginx
  labels:
    telepresence: random_id_123
spec:
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
        image: datawire/telepresence-k8s:0.777
        terminationMessagePolicy: "FallbackToLogsOnError"
        imagePullPolicy: "IfNotPresent"
        ports:
        - containerPort: 443
        - containerPort: 80
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
    original = yaml.safe_load(COMPLEX_DEPLOYMENT)
    expected = yaml.safe_load(SWAPPED_DEPLOYMENT)
    assert telepresence.new_swapped_deployment(
        original,
        "nginxhttps",
        "random_id_123",
        "datawire/telepresence-k8s:0.777",
        False,
        False,
    ) == (expected, original["spec"]["template"]["spec"]["containers"][1])


def test_portmapping():
    """
    Manually set exposed ports always override automatically exposed ports.
    """
    ports = telepresence.PortMapping.parse(["1234:80", "90"])
    ports.merge_automatic_ports([80, 555, 666])
    assert ports.local_to_remote() == {(1234, 80), (90, 90), (555, 555),
                                       (666, 666)}
    assert ports.remote() == {80, 90, 555, 666}


# Generate a random IPv4 as a string:
ip = st.integers(
    min_value=0, max_value=2**32 - 1
).map(lambda i: str(ipaddress.IPv4Address(i)))
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
    cidr = telepresence.covering_cidr(ips)
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


def test_runner_file():
    """Test some reasonable values for the log file"""
    # stdout
    lf_dash = telepresence.Runner.open("-", "kubectl", False)
    assert lf_dash.logfile is sys.stdout, lf_dash.logfile
    # /dev/null -- just make sure we don't crash
    telepresence.Runner.open("/dev/null", "kubectl", False)
    # Regular file -- make sure the file has been truncated
    o_content = "original content\n"
    with tempfile.NamedTemporaryFile(mode="w", delete=False) as out:
        out.write(o_content + "This should be truncated away.\nThis too.\n")
    lf_file = telepresence.Runner.open(out.name, "kubectl", False)
    n_content = "replacement content\n"
    lf_file.write(n_content)
    with open(out.name) as in_again:
        read_content = in_again.read()
        assert o_content not in read_content, read_content
        assert n_content in read_content, read_content
