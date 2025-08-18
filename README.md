# Telepresence: fast, efficient local development for Kubernetes microservices

[<img src="https://raw.githubusercontent.com/telepresenceio/telepresence.io/master/src/assets/images/telepresence-edgy.svg" width="80"/>](https://raw.githubusercontent.com/telepresenceio/telepresence.io/master/src/assets/images/telepresence-edgy.svg)

[![Artifact Hub](https://img.shields.io/endpoint?url=https://artifacthub.io/badge/repository/telepresence-oss)](https://artifacthub.io/packages/search?repo=telepresence-oss) [![Gurubase](https://img.shields.io/badge/Gurubase-Ask%20Telepresence%20Guru-006BFF)](https://gurubase.io/g/telepresence)

Telepresence gives developers infinite scale development environments for Kubernetes.

## Key benefits

**With Telepresence:**

* You run your services locally, using your favorite IDE and other tools
* Your workstation is connected to the cluster and can access to its services

**This gives developers:**

* A fast local dev loop, with no waiting for a container build / push / deploy
* Ability to use their favorite local tools (IDE, debugger, etc.)
* Ability to run large-scale applications that can't run locally

## Quick Start

A few quick ways to start using Telepresence:

* **Telepresence Quick Start:** [Quick Start](https://telepresence.io/docs/quick-start)
* **Install Telepresence:** [Install](https://telepresence.io/docs/install/client)
* **Contributor's Guide:** [Guide](https://github.com/telepresenceio/telepresence/blob/release/v2/CONTRIBUTING.md)
* **Meetings:** Check out our community [meeting schedule](https://github.com/telepresenceio/telepresence/blob/release/v2/MEETING_SCHEDULE.md) for opportunities to interact with Telepresence developers

## Walkthrough

### Install something in the cluster that Telepresence can engage with:
Start with an empty cluster:

```console
$ kubectl create deploy hello --image=k8s.gcr.io/echoserver:1.9
deployment.apps/hello created
$ kubectl expose deploy hello --port 80 --target-port 8080
service/hello exposed
$ kubectl get ns,svc,deploy,po
NAME                        STATUS   AGE
namespace/default           Active   4d19h
namespace/kube-node-lease   Active   4d19h
namespace/kube-public       Active   4d19h
namespace/kube-system       Active   4d19h

NAME                 TYPE        CLUSTER-IP      EXTERNAL-IP   PORT(S)   AGE
service/hello        ClusterIP   10.98.148.129   <none>        80/TCP    112s
service/kubernetes   ClusterIP   10.96.0.1       <none>        443/TCP   4d19h

NAME                    READY   UP-TO-DATE   AVAILABLE   AGE
deployment.apps/hello   1/1     1            1           2m47s

NAME                        READY   STATUS    RESTARTS   AGE
pod/hello-87f7f548f-djc8v   1/1     Running   0          1m47s
```

Check telepresence version
```console
$ telepresence version
OSS Client : v2.23.0
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
 ✔ Connected to context rancher-desktop, namespace default (https://127.0.0.1:6443)       2.4s 
```

A session is now active and outbound connections will be routed to the cluster. I.e. your laptop is logically "inside"
a namespace in the cluster.

Since telepresence connected to the default namespace, all services in that namespace can now be reached directly
by their name. You can of course also use namespaced names, e.g. `curl hello.default`.

```console
$ curl hello

Hostname: hello-87f7f548f-djc8v

Pod Information:
	-no pod information available-

Server values:
	server_version=nginx: 1.13.3 - lua: 10008

Request Information:
	client_address=10.1.5.190
	method=GET
	real path=/
	query=
	request_version=1.1
	request_scheme=http
	request_uri=http://hello:8080/

Request Headers:
	accept=*/*
	host=hello
	user-agent=curl/8.9.1

Request Body:
	-no body in request-
```

### Intercept the service. I.e. redirect traffic to it to our laptop (inbound traffic)

Add an intercept for the hello deployment on port 9000. Here, we also start a service listening on that port:

```console
$ telepresence intercept hello --port 9000 -- python3 -m http.server 9000
 ✔ Intercepted                                                                              2.1s 
Using Deployment hello
   Intercept name    : hello
   State             : ACTIVE
   Workload kind     : Deployment
   Intercepting      : 10.1.5.196 -> 127.0.0.1
       8080 -> 9000 TCP
   Volume Mount Point: /tmp/telfs-629530207
Serving HTTP on 0.0.0.0 port 9000 (http://0.0.0.0:9000/) ...
```

The `python -m httpserver` is now started on port 9000 and will run until terminated by `<ctrl>-C`. Access it from a browser using `http://hello/` or use curl from another terminal. With curl, it presents a html listing from the directory where the server was started. Something like:
```console
$ curl hello
<!DOCTYPE HTML>
<html lang="en">
<head>
<meta charset="utf-8">
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
127.0.0.1 - - [03/Apr/2025 09:44:57] "GET / HTTP/1.1" 200 -
```

### Clean-up and close daemon processes

End the service with `<ctrl>-C` and then try `curl hello` or `http://hello` again. The intercept is gone, and the echo service responds as normal.

Now end the session too. Your desktop no longer has access to the cluster internals.
```console
$ telepresence quit
 ✔ Disconnected                                                                           0.1s 
$ curl hello
curl: (6) Could not resolve host: hello
```

The telepresence daemons are still running in the background, which is harmless. You'll need to stop them before you
upgrade telepresence. That's done by passing the option `-s` (stop all local telepresence daemons) to the
quit command.

```console
$ telepresence quit -s
 ✔ Quit                                                                                   0.3s 
```

### What got installed in the cluster?

Telepresence installs the Traffic Manager in your cluster if it is not already present. This deployment remains unless you uninstall it.

Telepresence injects the Traffic Agent as an additional container into the pods of the workload you intercept, and  will optionally install
an init-container to route traffic through the agent (the init-container is only injected when the service is headless or uses a numerical
`targetPort`). The modifications persist unless you uninstall them.

At first glance, we can see that the deployment is installed ...
```console
$ kubectl get svc,deploy,pod
NAME                 TYPE        CLUSTER-IP      EXTERNAL-IP   PORT(S)   AGE
service/hello        ClusterIP   10.102.244.61   <none>        80/TCP    10m
service/kubernetes   ClusterIP   10.96.0.1       <none>        443/TCP   4d20h

NAME                    READY   UP-TO-DATE   AVAILABLE   AGE
deployment.apps/hello   1/1     1            1           11m

NAME                        READY   STATUS    RESTARTS   AGE
pod/hello-87f7f548f-mdg8d   2/2     Running   0          6m36s
```

... and that the traffic-manager is installed in the "ambassador" namespace.

```console
$ kubectl -n ambassador get svc,deploy,pod
NAME                      TYPE        CLUSTER-IP      EXTERNAL-IP   PORT(S)    AGE
service/agent-injector    ClusterIP   10.107.17.143   <none>        8443/TCP   31m
service/traffic-manager   ClusterIP   None            <none>        8081/TCP   31m

NAME                              READY   UP-TO-DATE   AVAILABLE   AGE
deployment.apps/traffic-manager   1/1     1            1           31m

NAME                                   READY   STATUS    RESTARTS   AGE
pod/traffic-manager-7cc6668576-hmlzz   1/1     Running   0          31m
```

The traffic-agent is installed too, in the hello pod. Here together with an init-container, because the service is using a numerical
`targetPort`.

```console
$ kubectl describe pod hello-774455b6f5-6x6vs
Name:             hello-87f7f548f-mdg8d
Namespace:        default
Priority:         0
Service Account:  default
Node:             lima-rancher-desktop/192.168.65.3
Start Time:       Thu, 03 Apr 2025 09:43:37 +0200
Labels:           app=hello
                  pod-template-hash=87f7f548f
                  telepresence.io/workloadEnabled=true
                  telepresence.io/workloadKind=Deployment
                  telepresence.io/workloadName=hello
Annotations:      telepresence.io/agent-config:
                    {"agentName":"hello","namespace":"default","logLevel":"debug","workloadName":"hello","workloadKind":"Deployment","managerHost":"traffic-ma...
                  telepresence.io/inject-traffic-agent: enabled
Status:           Running
IP:               10.1.5.196
IPs:
  IP:           10.1.5.196
Controlled By:  ReplicaSet/hello-87f7f548f
Init Containers:
  tel-agent-init:
    Container ID:  docker://f3203943fb97414bee8c3ad4b11237895a8165df7aa39a8f88741b4093e491be
    Image:         local/tel2:2.23.0-alpha.0
    Image ID:      docker-pullable://tel2@sha256:0f81a553bb223f4cfe97973d585586439451e120eb2ed8e35d0fe9266b22fd6d
    Port:          <none>
    Host Port:     <none>
    Args:
      agent-init
    State:          Terminated
      Reason:       Completed
      Exit Code:    0
      Started:      Thu, 03 Apr 2025 09:43:38 +0200
      Finished:     Thu, 03 Apr 2025 09:43:38 +0200
    Ready:          True
    Restart Count:  0
    Environment:
      LOG_LEVEL:     debug
      AGENT_CONFIG:   (v1:metadata.annotations['telepresence.io/agent-config'])
      POD_IP:         (v1:status.podIP)
    Mounts:
      /var/run/secrets/kubernetes.io/serviceaccount from kube-api-access-zgfs5 (ro)
Containers:
  echoserver:
    Container ID:   docker://2ccc7a81bfe7d1f666af7b17c6415631af2f1bfdb6cb147a0ef7a345f528ac49
    Image:          registry.k8s.io/echoserver:1.9
    Image ID:       docker-pullable://registry.k8s.io/echoserver@sha256:10f4dbc8eeeb8806d9b3a261b2473b77ca357b290a15d91ce5a0ca5e6164b535
    Port:           <none>
    Host Port:      <none>
    State:          Running
      Started:      Thu, 03 Apr 2025 09:43:39 +0200
    Ready:          True
    Restart Count:  0
    Environment:    <none>
    Mounts:
      /var/run/secrets/kubernetes.io/serviceaccount from kube-api-access-zgfs5 (ro)
  traffic-agent:
    Container ID:  docker://b4f2279e58aacdf3426c80381c29f7cc214729a7d44a40acd1a566d778d84cfa
    Image:         local/tel2:2.23.0-alpha.0
    Image ID:      docker-pullable://tel2@sha256:0f81a553bb223f4cfe97973d585586439451e120eb2ed8e35d0fe9266b22fd6d
    Port:          9900/TCP
    Host Port:     0/TCP
    Args:
      agent
    State:          Running
      Started:      Thu, 03 Apr 2025 09:43:39 +0200
    Ready:          True
    Restart Count:  0
    Readiness:      exec [/bin/stat /tmp/agent/ready] delay=0s timeout=1s period=10s #success=1 #failure=3
    Environment:
      AGENT_CONFIG:         (v1:metadata.annotations['telepresence.io/agent-config'])
      _TEL_AGENT_POD_IP:    (v1:status.podIP)
      _TEL_AGENT_POD_UID:   (v1:metadata.uid)
      _TEL_AGENT_NAME:     hello-87f7f548f-mdg8d (v1:metadata.name)
    Mounts:
      /tel_app_exports from export-volume (rw)
      /tel_app_mounts/echoserver/var/run/secrets/kubernetes.io/serviceaccount from kube-api-access-zgfs5 (ro)
      /tmp from tel-agent-tmp (rw)
      /var/run/secrets/kubernetes.io/serviceaccount from kube-api-access-zgfs5 (ro)
Conditions:
  Type                        Status
  PodReadyToStartContainers   True 
  Initialized                 True 
  Ready                       True 
  ContainersReady             True 
  PodScheduled                True 
Volumes:
  kube-api-access-zgfs5:
    Type:                    Projected (a volume that contains injected data from multiple sources)
    TokenExpirationSeconds:  3607
    ConfigMapName:           kube-root-ca.crt
    ConfigMapOptional:       <nil>
    DownwardAPI:             true
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
  Normal  Scheduled  8m     default-scheduler  Successfully assigned default/hello-87f7f548f-mdg8d to lima-rancher-desktop
  Normal  Pulled     7m59s  kubelet            Container image "local/tel2:2.23.0-alpha.0" already present on machine
  Normal  Created    7m59s  kubelet            Created container: tel-agent-init
  Normal  Started    7m59s  kubelet            Started container tel-agent-init
  Normal  Pulled     7m58s  kubelet            Container image "registry.k8s.io/echoserver:1.9" already present on machine
  Normal  Created    7m58s  kubelet            Created container: echoserver
  Normal  Started    7m58s  kubelet            Started container echoserver
  Normal  Pulled     7m58s  kubelet            Container image "local/tel2:2.23.0-alpha.0" already present on machine
  Normal  Created    7m58s  kubelet            Created container: traffic-agent
  Normal  Started    7m58s  kubelet            Started container traffic-agent
```

### Uninstalling

You can uninstall the traffic-agent from specific deployments or from all deployments. Or you can choose to uninstall everything in which
case the traffic-manager and all traffic-agents will be uninstalled.

```console
$ telepresence helm uninstall
```
will remove everything that was automatically installed by telepresence from the cluster.

```console
$ telepresence uninstall hello
```
will remove the traffic-agent and the configmap entry.

### Troubleshooting

The telepresence background processes `daemon` and `connector` both produces log files that can be very helpful when problems are
encountered. The files are named `daemon.log` and `connector.log`. The location of the logs differ depending on what platform that is used:

- macOS `~/Library/Logs/telepresence`
- Linux `~/.cache/telepresence/logs`
- Windows `"%USERPROFILE%\AppData\Local\logs"`

## How it works

When Telepresence 2 connects to a Kubernetes cluster, it:

 1. Ensures Traffic Manager is installed in the cluster.
 2. Looks for the relevant subnets in the kubernetes cluster.
 3. Creates a Virtual Network Interface (VIF).
 4. Assigns the cluster's subnets to the VIF.
 5. Binds itself to VIF and starts routing traffic to the traffic-manager, or a traffic-agent if one is present.
 6. Starts listening for, and serving DNS requests, by passing a selected portion to the traffic-manager or traffic-agent.

When a locally running application makes a network request to a service in the cluster, Telepresence will resolve the name to an address within the cluster.
The operating system then sees that the TUN device has an address in the same subnet as the address of the outgoing packets and sends them to `tel0`.
Telepresence is on the other side of `tel0` and picks up the packets, injecting them into the cluster through a gRPC connection with Traffic Manager.

## Troubleshooting

Visit the troubleshooting section in the Telepresence documentation for more advice:
[Troubleshooting](https://telepresence.io/docs/troubleshooting/)

Or discuss with the community in the [CNCF Slack](https://communityinviter.com/apps/cloud-native/cncf) in the [#telepresence-oss](https://cloud-native.slack.com/archives/C06B36KJ85P) channel.


