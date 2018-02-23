# Troubleshooting & Workarounds

### Method-specific limitations

For method-specific limitations see the documentation on the [available proxying methods](/reference/methods.html).

### General limitations & workarounds

#### Docker containers

When using `--method vpn-tcp` or `--method inject-tcp` a container run via `docker run` will not inherit the outgoing functionality of the Telepresence shell.
If you want to use Telepresence to proxy a containerized application you should use [`--method container`](/tutorials/docker.html).

#### `localhost` and the pod

`localhost` and `127.0.0.1` will end up accessing the host machine—the machine where you run `telepresence`—*not* the pod.
This can be a problem in cases where you are running multiple containers in a pod and you need your process to access a different container in the same pod.

The solution is to access the pod via its IP, rather than at `127.0.0.1`.
You can have the pod IP configured as an environment variable `$MY_POD_IP` in the Deployment using the Kubernetes [Downward API](https://kubernetes.io/docs/tasks/configure-pod-container/environment-variable-expose-pod-information/):

<pre><code class="lang-yaml">apiVersion: extensions/v1beta1
kind: Deployment
spec:
  template:
    spec:
      containers:
      - name: servicename
        image: datawire/telepresence-k8s:{{ book['version'] }}
        env:
        - name: MY_POD_IP
          valueFrom:
            fieldRef:
              fieldPath: status.podIP
</code></pre>

#### EC2

Amazon EC2 instances inside a VPC use a custom DNS setup that resolves internal names. This will prevent Telepresence from working properly. To resolve this issue, override the default name servers, e.g.,

```
sudo echo 'supersede domain-name-servers 8.8.8.8, 8.8.4.4;' >> /etc/dhcp/dhclient.conf
sudo dhclient
```

For more details see [issue # 462](https://github.com/datawire/telepresence/issues/462).

#### Fedora 18+/CentOS 7+/RHEL 7+ and `--docker-run`

Fedora 18+/CentOS 7+/RHEL 7+ ship with firewalld enabled and running by default. In its default configuration this will drop traffic on unknown ports originating from Docker's default bridge network - usually `172.17.0.0/16`. 

To resolve this issue, instruct firewalld to trust traffic from `172.17.0.0/16`:

```
sudo firewall-cmd --permanent --zone=trusted --add-source=172.17.0.0/16
sudo firewall-cmd --reload
```

For more details see [issue # 464](https://github.com/datawire/telepresence/issues/464).
