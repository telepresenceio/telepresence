# Telepresence 2

This is internal to Ambassador Labs.

## Walkthrough

### Install an interceptable service:
Start with an empty cluster:

```console
$ kubectl create deploy hello --image=k8s.gcr.io/echoserver:1.4
deployment.apps/hello created
$ kubectl expose deploy hello --port 80 --target-port 8080
service/hello exposed
$ kubectl get ns,svc,deploy,po
NAME                        STATUS   AGE
namespace/default           Active   27d
namespace/kube-system       Active   27d
namespace/kube-public       Active   27d
namespace/kube-node-lease   Active   27d

NAME                 TYPE        CLUSTER-IP     EXTERNAL-IP   PORT(S)   AGE
service/kubernetes   ClusterIP   10.43.0.1      <none>        443/TCP   27d
service/hello        ClusterIP   10.43.197.70   <none>        80/TCP    12s

NAME                    READY   UP-TO-DATE   AVAILABLE   AGE
deployment.apps/hello   1/1     1            1           18s

NAME                        READY   STATUS    RESTARTS   AGE
pod/hello-9954f98bf-rbwmz   1/1     Running   0          18s
```

Check telepresence version
```console
$ telepresence version
Client v0.3.0 (api v3)
```

### Establish a connection to  the cluster (outbound traffic)

Let telepresence connect:
```console
$ telepresence connect
Launching Telepresence Daemon v0.4.0 (api v3)
Connecting to traffic manager...
Connected to context default (https://35.232.104.64)
```

A session is now active and outbound connections will be routed to the cluster. I.e. your laptop is "inside" the cluster.

```console
$ curl hello
CLIENT VALUES:
client_address=10.42.0.15
command=GET
real path=/
query=nil
request_version=1.1
request_uri=http://hello:8080/

SERVER VALUES:
server_version=nginx: 1.10.0 - lua: 10001

HEADERS RECEIVED:
accept=*/*
host=hello
user-agent=curl/7.71.1
BODY:
-no body in request-
```

### Intercept the service. I.e. redirect traffic to it to our laptop (inbound traffic)

Add an intercept for the hello deployment on port 9000. Here, we also start a service listening on that port:

```console
$ telepresence intercept hello --port 9000 -- python3 -m http.server 9000
Already connected
Using deployment hello
Serving HTTP on 0.0.0.0 port 9000 (http://0.0.0.0:9000/) ...
```

The `python -m httpserver` is now started on port 9000 and will run until terminated by `<ctrl>-C`. Access it from a browser using `http://hello/` or use curl from another terminal. With curl, it presents a html listing from the directory where the server was started. Something like:
```console
$ curl hello
<!DOCTYPE HTML PUBLIC "-//W3C//DTD HTML 4.01//EN" "http://www.w3.org/TR/html4/strict.dtd">
<html>
<head>
<meta http-equiv="Content-Type" content="text/html; charset=utf-8">
<title>Directory listing for /</title>
</head>
<body>
<h1>Directory listing for /</h1>
<hr>
<ul>
<li><a href="file1.txt">file1.txt</a></li>
<li><a href="file2.txt">file2.txt</a></li>
</ul>
<hr>
</body>
</html>
```

Observe that the python service reports that it's being accessed:
```console
127.0.0.1 - - [26/Nov/2020 13:11:19] "GET / HTTP/1.1" 200 -
127.0.0.1 - - [26/Nov/2020 13:18:28] "GET / HTTP/1.1" 200 -
```

### Clean-up and close daemon processes

End the service with `<ctrl>-C` and then try `curl hello` or `http://hello` again. The intercept is gone, and the echo service responds as normal.

Now end the session too. Your laptop no longer has access to the cluster internals.
```console
$ telepresence quit
Telepresence Daemon quitting...done
$ curl hello
curl: (6) Could not resolve host: hello
```

### Start outbound and intercept with one single command

There is no need to start a telepresence subshell when doing an intercept. Telepresence will automatically detect that a session is active, and if not, start one. The session then ends when the command exits, as shown in this example:

```console
telepresence intercept hello --port 9000 -- python3 -m http.server 9000

Launching Telepresence Daemon v0.3.0 (api v3)
Connecting to traffic manager...
Connected to context default (https://34.123.86.205)
Using deployment hello
Serving HTTP on 0.0.0.0 port 9000 (http://0.0.0.0:9000/) ...
127.0.0.1 - - [26/Nov/2020 14:35:07] "GET / HTTP/1.1" 200 -
^C
Keyboard interrupt received, exiting.
Disconnecting...done
Telepresence Daemon quitting...done
```

### What got installed in the cluster?

At first glance, we can see that the traffic-manager service and deployment are installed.
```console
kubectl get svc,deploy,pod
NAME                      TYPE        CLUSTER-IP     EXTERNAL-IP   PORT(S)             AGE
service/kubernetes        ClusterIP   10.43.0.1      <none>        443/TCP             27d
service/traffic-manager   ClusterIP   None           <none>        8022/TCP,8081/TCP   3h13m
service/hello             ClusterIP   10.43.197.70   <none>        80/TCP              3h14m

NAME                              READY   UP-TO-DATE   AVAILABLE   AGE
deployment.apps/hello             1/1     1            1           3h14m
deployment.apps/traffic-manager   1/1     1            1           3h13m

NAME                                   READY   STATUS    RESTARTS   AGE
pod/hello-549bc44bc5-zdvdb             2/2     Running   1          3h10m
pod/traffic-manager-7ccdb778f8-66zjm   1/1     Running   0          54m
```

The traffic-agent is installed too, in the hello pod.

```console
 kubectl describe pod hello-87f6cb877-82pll
Name:         hello-87f6cb877-82pll
Namespace:    default
Priority:     0
Node:         thallgren-edge/10.88.6.6
Start Time:   Thu, 26 Nov 2020 16:24:04 +0100
Labels:       app=hello
              pod-template-hash=87f6cb877
Annotations:  <none>
Status:       Running
IP:           10.42.0.20
IPs:
  IP:           10.42.0.20
Controlled By:  ReplicaSet/hello-87f6cb877
Containers:
  echoserver:
    Container ID:   containerd://99b130467cd04fabcc28391125d7ad4a8240f06237a2b515fdae1233274cf891
    Image:          k8s.gcr.io/echoserver:1.4
    Image ID:       sha256:523cad1a4df732d41406c9de49f932cd60d56ffd50619158a2977fd1066028f9
    Port:           <none>
    Host Port:      <none>
    State:          Running
      Started:      Thu, 26 Nov 2020 16:24:05 +0100
    Ready:          True
    Restart Count:  0
    Environment:    <none>
    Mounts:
      /var/run/secrets/kubernetes.io/serviceaccount from default-token-blr2m (ro)
  traffic-agent:
    Container ID:  containerd://4fddaef430252449a445c6f140736a22c00389ccc81840cc4cc15b69bb449688
    Image:         docker.io/datawire/tel2:v0.3.0
    Image ID:      docker.io/datawire/tel2@sha256:2f91295b0e4de5956f9e1c771da9d06a399ba88cb4392cf7af677bd20f685f36
    Port:          9900/TCP
    Host Port:     0/TCP
    Args:
      agent
    State:          Running
      Started:      Thu, 26 Nov 2020 16:24:05 +0100
    Ready:          True
    Restart Count:  0
    Environment:
      LOG_LEVEL:   debug
      AGENT_NAME:  hello
      APP_PORT:    8080
    Mounts:
      /var/run/secrets/kubernetes.io/serviceaccount from default-token-blr2m (ro)
Conditions:
  Type              Status
  Initialized       True
  Ready             True
  ContainersReady   True
  PodScheduled      True
Volumes:
  default-token-blr2m:
    Type:        Secret (a volume populated by a Secret)
    SecretName:  default-token-blr2m
    Optional:    false
QoS Class:       BestEffort
Node-Selectors:  <none>
Tolerations:     node.kubernetes.io/not-ready:NoExecute op=Exists for 300s
                 node.kubernetes.io/unreachable:NoExecute op=Exists for 300s
Events:
  Type    Reason     Age    From               Message
  ----    ------     ----   ----               -------
  Normal  Scheduled  3m20s  default-scheduler  Successfully assigned default/hello-87f6cb877-82pll to thallgren-edge
  Normal  Pulled     3m20s  kubelet            Container image "k8s.gcr.io/echoserver:1.4" already present on machine
  Normal  Created    3m20s  kubelet            Created container echoserver
  Normal  Started    3m20s  kubelet            Started container echoserver
  Normal  Pulled     3m20s  kubelet            Container image "docker.io/datawire/tel2:v0.3.0" already present on machine
  Normal  Created    3m20s  kubelet            Created container traffic-agent
  Normal  Started    3m20s  kubelet            Started container traffic-agent
```

### Troubleshooting

The telepresence background processes `daemon` and `commector`both produces log files that can be very helpful when problems are encountered. The files are named `daemon.log` and `connector.log`. The location of the logs differ depending on what platform that is used:

- MacOS `~/Library/Logs/telepresence`
- Linux `~/.cache/telepresence/logs` 

## Comparison to classic Telepresence

Telepresence will launch your command or a shell when you start a session. When that program ends, the session ends and Telepresence cleans up.

### What works

- Outbound: You can `curl` a service running in the cluster while a session is running
- Inbound: You can intercept a deployment, causing all requests to that deployment to go to your laptop instead

### What doesn't work yet

- Environment variables
- Filesystem forwarding for volume mounts
- Container method
- The `--also-proxy` feature

### What behaves differently

Telepresence installs the Traffic Manager in your cluster if it is not already present. This deployment remains, i.e. does not get cleaned up.

Telepresence installs the Traffic Agent as an additional container in any deployment you intercept, and modifies any associated services it finds to route traffic through the agent. This modification persists, i.e. does not get cleaned up.

You can launch other Telepresence sessions to the same cluster while an existing session is running, letting you intercept other deployments.
