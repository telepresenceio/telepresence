# Telepresence: fast, efficient local development for Kubernetes microservices

[<img src="https://cncf-branding.netlify.app/img/projects/telepresence/horizontal/color/telepresence-horizontal-color.png" width="80"/>](https://cncf-branding.netlify.app/img/projects/telepresence/horizontal/color/telepresence-horizontal-color.png)

Telepresence gives developers infinite scale development environments for Kubernetes.

Docs:
    OSS: [https://www.getambassador.io/docs/telepresence-oss/](https://www.getambassador.io/docs/telepresence-oss)
    Licensed: [https://www.getambassador.io/docs/telepresence ](https://www.getambassador.io/docs/telepresence )
Slack:
    Discuss in the [OSS CNCF Slack](https://communityinviter.com/apps/cloud-native/cncf) in the [#telepresence-oss](https://cloud-native.slack.com/archives/C06B36KJ85P) channel
    Licensed: [a8r.io/slack](https://a8r.io/slack)

**With Telepresence:**

* You run one service locally, using your favorite IDE and other tools
* You run the rest of your application in the [cloud](https://www.getambassador.io/products/ambassador-cloud/), where there is unlimited memory and compute

**This gives developers:**

* A fast local dev loop, with no waiting for a container build / push / deploy
* Ability to use their favorite local tools (IDE, debugger, etc.)
* Ability to run large-scale applications that can't run locally

## Quick Start

A few quick ways to start using Telepresence

* **Telepresence Quick Start:** [Quick Start](https://www.getambassador.io/docs/telepresence/latest/quick-start/)
* **Install Telepresence:** [Install](https://www.getambassador.io/docs/telepresence/latest/install/)
* **Contributor's Guide:** [Guide](https://github.com/telepresenceio/telepresence/blob/release/v2/DEVELOPING.md)
* **Meetings:** Check out our community [meeting schedule](https://github.com/telepresenceio/telepresence/blob/release/v2/MEETING_SCHEDULE.md) for opportunities to interact with Telepresence developers

## Walkthrough

### Install an interceptable service:
Start with an empty cluster:

```console
$ kubectl create deploy hello --image=registry.k8s.io/echoserver:1.4
deployment.apps/hello created
$ kubectl expose deploy hello --port 80 --target-port 8080
service/hello exposed
$ kubectl get ns,svc,deploy,po
NAME                        STATUS   AGE
namespace/kube-system       Active   53m
namespace/default           Active   53m
namespace/kube-public       Active   53m
namespace/kube-node-lease   Active   53m

NAME                 TYPE        CLUSTER-IP     EXTERNAL-IP   PORT(S)   AGE
service/kubernetes   ClusterIP   10.43.0.1      <none>        443/TCP   53m
service/hello        ClusterIP   10.43.73.112   <none>        80/TCP    2m

NAME                    READY   UP-TO-DATE   AVAILABLE   AGE
deployment.apps/hello   1/1     1            1           2m

NAME                        READY   STATUS    RESTARTS   AGE
pod/hello-9954f98bf-6p2k9   1/1     Running   0          2m15s
```

Check telepresence version
```console
$ telepresence version
OSS Client : v2.17.0
Root Daemon: not running
User Daemon: not running
```

### Setup Traffic Manager in the cluster

Install Traffic Manager in your cluster. By default, it will reside in the `ambassador` namespace:
```console
$ telepresence helm install

Traffic Manager installed successfully
```

### Establish a connection to  the cluster (outbound traffic)

Let telepresence connect:
```console
$ telepresence connect
Launching Telepresence Root Daemon
Launching Telepresence User Daemon
Connected to context default, namespace default (https://35.232.104.64)
```

A session is now active and outbound connections will be routed to the cluster. I.e. your laptop is logically "inside"
a namespace in the cluster.

Since telepresence connected to the default namespace, all services in that namespace can now be reached directly
by their name. You can of course also use namespaced names, e.g. `curl hello.default`.

```console
$ curl hello
CLIENT VALUES:
client_address=10.244.0.87
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
user-agent=curl/8.0.1
BODY:
-no body in request-
```

### Intercept the service. I.e. redirect traffic to it to our laptop (inbound traffic)

Add an intercept for the hello deployment on port 9000. Here, we also start a service listening on that port:

```console
$ telepresence intercept hello --port 9000 -- python3 -m http.server 9000
Using Deployment hello
intercepted
    Intercept name         : hello
    State                  : ACTIVE
    Workload kind          : Deployment
    Destination            : 127.0.0.1:9000
    Service Port Identifier: 80
    Volume Mount Point     : /tmp/telfs-524630891
    Intercepting           : all TCP connections
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
```
127.0.0.1 - - [16/Jun/2022 11:39:20] "GET / HTTP/1.1" 200 -
```

### Clean-up and close daemon processes

End the service with `<ctrl>-C` and then try `curl hello` or `http://hello` again. The intercept is gone, and the echo service responds as normal.

Now end the session too. Your desktop no longer has access to the cluster internals.
```console
$ telepresence quit
Disconnected
$ curl hello
curl: (6) Could not resolve host: hello
```

The telepresence daemons are still running in the background, which is harmless. You'll need to stop them before you
upgrade telepresence. That's done by passing the option `-s` (stop all local telepresence daemons) to the
quit command.

```console
$ telepresence quit -s
Telepresence Daemons quitting...done
```

### What got installed in the cluster?

Telepresence installs the Traffic Manager in your cluster if it is not already present. This deployment remains unless you uninstall it.

Telepresence injects the Traffic Agent as an additional container into the pods of the workload you intercept, and  will optionally install
an init-container to route traffic through the agent (the init-container is only injected when the service is headless or uses a numerical
`targetPort`). The modifications persist unless you uninstall them.

At first glance, we can see that the deployment is installed ...
```console
$ kubectl get svc,deploy,pod
service/kubernetes   ClusterIP   10.43.0.1       <none>        443/TCP                      7d22h
service/hello        ClusterIP   10.43.145.57    <none>        80/TCP                       13m

NAME                    READY   UP-TO-DATE   AVAILABLE   AGE
deployment.apps/hello   1/1     1            1           13m

NAME                         READY   STATUS    RESTARTS        AGE
pod/hello-774455b6f5-6x6vs   2/2     Running   0               10m
```

... and that the traffic-manager is installed in the "ambassador" namespace.

```console
$ kubectl -n ambassador get svc,deploy,pod
NAME                      TYPE        CLUSTER-IP     EXTERNAL-IP   PORT(S)    AGE
service/traffic-manager   ClusterIP   None           <none>        8081/TCP   17m
service/agent-injector    ClusterIP   10.43.72.154   <none>        443/TCP    17m

NAME                              READY   UP-TO-DATE   AVAILABLE   AGE
deployment.apps/traffic-manager   1/1     1            1           17m

NAME                                  READY   STATUS    RESTARTS   AGE
pod/traffic-manager-dcd4cc64f-6v5bp   1/1     Running   0          17m
```

The traffic-agent is installed too, in the hello pod. Here together with an init-container, because the service is using a numerical
`targetPort`.

```console
$ kubectl describe pod hello-774455b6f5-6x6vs
Name:             hello-75b7c6d484-9r4xd
Namespace:        default
Priority:         0
Service Account:  default
Node:             kind-control-plane/192.168.96.2
Start Time:       Sun, 07 Jan 2024 01:01:33 +0100
Labels:           app=hello
                  pod-template-hash=75b7c6d484
                  telepresence.io/workloadEnabled=true
                  telepresence.io/workloadName=hello
Annotations:      telepresence.getambassador.io/inject-traffic-agent: enabled
                  telepresence.getambassador.io/restartedAt: 2024-01-07T00:01:33Z
Status:           Running
IP:               10.244.0.89
IPs:
  IP:           10.244.0.89
Controlled By:  ReplicaSet/hello-75b7c6d484
Init Containers:
  tel-agent-init:
    Container ID:  containerd://4acdf45992980e2796f0eb79fb41afb1a57808d108eb14a355cb390ccc764571
    Image:         docker.io/datawire/tel2:2.17.0
    Image ID:      docker.io/datawire/tel2@sha256:e18aed6e7bd3c15cb5a99161c164e0303d20156af68ef138faca98dc2c5754a7
    Port:          <none>
    Host Port:     <none>
    Args:
      agent-init
    State:          Terminated
      Reason:       Completed
      Exit Code:    0
      Started:      Sun, 07 Jan 2024 01:01:34 +0100
      Finished:     Sun, 07 Jan 2024 01:01:34 +0100
    Ready:          True
    Restart Count:  0
    Environment:    <none>
    Mounts:
      /etc/traffic-agent from traffic-config (rw)
      /var/run/secrets/kubernetes.io/serviceaccount from kube-api-access-svf4h (ro)
Containers:
  echoserver:
    Container ID:   containerd://577e140545f3106c90078e687e0db3661db815062084bb0c9f6b2d0b4f949308
    Image:          registry.k8s.io/echoserver:1.4
    Image ID:       sha256:523cad1a4df732d41406c9de49f932cd60d56ffd50619158a2977fd1066028f9
    Port:           <none>
    Host Port:      <none>
    State:          Running
      Started:      Sun, 07 Jan 2024 01:01:34 +0100
    Ready:          True
    Restart Count:  0
    Environment:    <none>
    Mounts:
      /var/run/secrets/kubernetes.io/serviceaccount from kube-api-access-svf4h (ro)
  traffic-agent:
    Container ID:  containerd://17558b4711903f4cb580c5afafa169d314a7deaf33faa749f59d3a2f8eed80a9
    Image:         docker.io/datawire/tel2:2.17.0
    Image ID:      docker.io/datawire/tel2@sha256:e18aed6e7bd3c15cb5a99161c164e0303d20156af68ef138faca98dc2c5754a7
    Port:          9900/TCP
    Host Port:     0/TCP
    Args:
      agent
    State:          Running
      Started:      Sun, 07 Jan 2024 01:01:34 +0100
    Ready:          True
    Restart Count:  0
    Readiness:      exec [/bin/stat /tmp/agent/ready] delay=0s timeout=1s period=10s #success=1 #failure=3
    Environment:
      _TEL_AGENT_POD_IP:       (v1:status.podIP)
      _TEL_AGENT_NAME:        hello-75b7c6d484-9r4xd (v1:metadata.name)
      A_TELEPRESENCE_MOUNTS:  /var/run/secrets/kubernetes.io/serviceaccount
    Mounts:
      /etc/traffic-agent from traffic-config (rw)
      /tel_app_exports from export-volume (rw)
      /tel_app_mounts/echoserver/var/run/secrets/kubernetes.io/serviceaccount from kube-api-access-svf4h (ro)
      /tel_pod_info from traffic-annotations (rw)
      /tmp from tel-agent-tmp (rw)
      /var/run/secrets/kubernetes.io/serviceaccount from kube-api-access-svf4h (ro)
Conditions:
  Type              Status
  Initialized       True 
  Ready             True 
  ContainersReady   True 
  PodScheduled      True 
Volumes:
  kube-api-access-svf4h:
    Type:                    Projected (a volume that contains injected data from multiple sources)
    TokenExpirationSeconds:  3607
    ConfigMapName:           kube-root-ca.crt
    ConfigMapOptional:       <nil>
    DownwardAPI:             true
  traffic-annotations:
    Type:  DownwardAPI (a volume populated by information about the pod)
    Items:
      metadata.annotations -> annotations
  traffic-config:
    Type:      ConfigMap (a volume populated by a ConfigMap)
    Name:      telepresence-agents
    Optional:  false
  export-volume:
    Type:       EmptyDir (a temporary directory that shares a pod's lifetime)
    Medium:     
    SizeLimit:  <unset>
  tel-agent-tmp:
    Type:        EmptyDir (a temporary directory that shares a pod's lifetime)
    Medium:      
    SizeLimit:   <unset>
QoS Class:       BestEffort
Node-Selectors:  <none>
Tolerations:     node.kubernetes.io/not-ready:NoExecute op=Exists for 300s
                 node.kubernetes.io/unreachable:NoExecute op=Exists for 300s
Events:
  Type    Reason     Age    From               Message
  ----    ------     ----   ----               -------
  Normal  Scheduled  7m40s  default-scheduler  Successfully assigned default/hello-75b7c6d484-9r4xd to kind-control-plane
  Normal  Pulled     7m40s  kubelet            Container image "docker.io/datawire/tel2:2.17.0" already present on machine
  Normal  Created    7m40s  kubelet            Created container tel-agent-init
  Normal  Started    7m39s  kubelet            Started container tel-agent-init
  Normal  Pulled     7m39s  kubelet            Container image "registry.k8s.io/echoserver:1.4" already present on machine
  Normal  Created    7m39s  kubelet            Created container echoserver
  Normal  Started    7m39s  kubelet            Started container echoserver
  Normal  Pulled     7m39s  kubelet            Container image "docker.io/datawire/tel2:2.17.0" already present on machine
  Normal  Created    7m39s  kubelet            Created container traffic-agent
  Normal  Started    7m39s  kubelet            Started container traffic-agent
```

Telepresence keeps track of all possible intercepts for containers that have an agent installed in the configmap `telepresence-agents`.  

```console
$ kubectl describe configmap telepresence-agents 
Name:         telepresence-agents
Namespace:    default
Labels:       app.kubernetes.io/created-by=traffic-manager
              app.kubernetes.io/name=telepresence-agents
              app.kubernetes.io/version=2.17.0
Annotations:  <none>

Data
====
hello:
----
agentImage: localhost:5000/tel2:2.17.0
agentName: hello
containers:
- Mounts: null
  envPrefix: A_
  intercepts:
  - agentPort: 9900
    containerPort: 8080
    protocol: TCP
    serviceName: hello
    servicePort: 80
    serviceUID: 68a4ecd7-0a12-44e2-9293-dc16fb205621
    targetPortNumeric: true
  mountPoint: /tel_app_mounts/echoserver
  name: echoserver
logLevel: debug
managerHost: traffic-manager.ambassador
managerPort: 8081
namespace: default
pullPolicy: IfNotPresent
tracingPort: 15766
workloadKind: Deployment
workloadName: hello


BinaryData
====

Events:  <none>
```

### Uninstalling

You can uninstall the traffic-agent from specific deployments or from all deployments. Or you can choose to uninstall everything in which
case the traffic-manager and all traffic-agents will be uninstalled.

```console
$ telepresence helm uninstall
```
will remove everything that was automatically installed by telepresence from the cluster.

```console
$ telepresence uninstall --agent hello
```
will remove the traffic-agent and the configmap entry.

### Troubleshooting

The telepresence background processes `daemon` and `connector` both produces log files that can be very helpful when problems are
encountered. The files are named `daemon.log` and `connector.log`. The location of the logs differ depending on what platform that is used:

- macOS `~/Library/Logs/telepresence`
- Linux `~/.cache/telepresence/logs`
- Windows `"%USERPROFILE%\AppData\Local\logs"`

Visit the troubleshooting section in the Telepresence documentation for more advice:
[Troubleshooting](https://www.getambassador.io/docs/telepresence/latest/troubleshooting/)
