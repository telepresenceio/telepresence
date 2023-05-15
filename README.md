# Telepresence 2: fast, efficient local development for Kubernetes microservices

[<img src="https://cncf-branding.netlify.app/img/projects/telepresence/horizontal/color/telepresence-horizontal-color.png" width="80"/>](https://cncf-branding.netlify.app/img/projects/telepresence/horizontal/color/telepresence-horizontal-color.png)

Telepresence gives developers infinite scale development environments for Kubernetes. 

Website: [https://www.getambassador.io/products/telepresence/](https://www.getambassador.io/products/telepresence/)  
Slack: [Discuss](https://a8r.io/slack) in the #telepresence channel (https://datawire-oss.slack.com/archives/CAUBBJSQZ)

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

## Documentation
Telepresence documentation is available on the Ambassador Labs webside:  
[Documentation](https://www.getambassador.io/docs/telepresence/)

## Telepresence 2

Telepresence 2 is based on learnings from the original Telepresence architecture. Rewritten in Go, Telepresence 2 provides a simpler and more powerful user experience, improved performance, and better reliability than Telepresence 1. More details on Telepresence 2 are below.


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
Client: v2.6.7 (api v3)
Root Daemon: v2.6.7 (api v3)
User Daemon: v2.6.7 (api v3)
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
Connected to context default (https://35.232.104.64)
```

A session is now active and outbound connections will be routed to the cluster. I.e. your laptop is "inside" the cluster.

```console
$ curl hello.default
CLIENT VALUES:
client_address=10.42.0.189
command=GET
real path=/
query=nil
request_version=1.1
request_uri=http://hello.default:8080/

SERVER VALUES:
server_version=nginx: 1.10.0 - lua: 10001

HEADERS RECEIVED:
accept=*/*
host=hello.default
user-agent=curl/7.79.1
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
    Intercepting           : all TCP requests
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

Since telepresence is now intercepting services in the default namespace, all services in that namespace can now be reached directly by their name. You can of course still use the namespaced name too, e.g. `curl hello.default`.

### Clean-up and close daemon processes

End the service with `<ctrl>-C` and then try `curl hello.default` or `http://hello.default` again. The intercept is gone, and the echo service responds as normal. Using just `curl hello` will no longer succeed. This is because telepresence stopped mapping the default namespace when there were no more intercepts using it.

Now end the session too. Your desktop no longer has access to the cluster internals.
```console
$ telepresence quit
Telepresence Network disconnecting...done
Telepresence Traffic Manager disconnecting...done
$ curl hello.default
curl: (6) Could not resolve host: hello.default
```

The telepresence daemons are still running in the background, which is harmless. You'll need to stop them before you
upgrade telepresence. That's done by passing the options `-u` (stop user daemon) and `-r` (stop root daemon) to the
quit command.

```console
$ telepresence quit -ur
Telepresence Network quitting...done
Telepresence Traffic Manager quitting...done
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
Name:         hello-774455b6f5-6x6vs
Namespace:    default
Priority:     0
Node:         multi/192.168.1.110
Start Time:   Thu, 16 Jun 2022 11:38:22 +0200
Labels:       app=hello
              pod-template-hash=774455b6f5
Annotations:  telepresence.getambassador.io/inject-traffic-agent: enabled
              telepresence.getambassador.io/restartedAt: 2022-06-16T09:38:21Z
Status:       Running
IP:           10.42.0.191
IPs:
  IP:           10.42.0.191
Controlled By:  ReplicaSet/hello-774455b6f5
Init Containers:
  tel-agent-init:
    Container ID:  containerd://e968352b3d85d6f966ac55f02da2401f93935f6df1f087b06bbe1cfc8854d5fb
    Image:         docker.io/datawire/ambassador-telepresence-agent:1.12.6
    Image ID:      docker.io/datawire/ambassador-telepresence-agent@sha256:2652d2767d1e8968be3fb22f365747315e25ac95e12c3d39f1206080a1e66af3
    Port:          <none>
    Host Port:     <none>
    Args:
      agent-init
    State:          Terminated
      Reason:       Completed
      Exit Code:    0
      Started:      Thu, 16 Jun 2022 11:38:39 +0200
      Finished:     Thu, 16 Jun 2022 11:38:39 +0200
    Ready:          True
    Restart Count:  0
    Environment:    <none>
    Mounts:
      /etc/traffic-agent from traffic-config (rw)
      /var/run/secrets/kubernetes.io/serviceaccount from kube-api-access-wzhhs (ro)
Containers:
  echoserver:
    Container ID:   containerd://80d4645769a06b8671b5a4ce29d28abfa72ce5659ba96916c231bb9629593a29
    Image:          registry.k8s.io/echoserver:1.4
    Image ID:       sha256:523cad1a4df732d41406c9de49f932cd60d56ffd50619158a2977fd1066028f9
    Port:           <none>
    Host Port:      <none>
    State:          Running
      Started:      Thu, 16 Jun 2022 11:38:40 +0200
    Ready:          True
    Restart Count:  0
    Environment:    <none>
    Mounts:
      /var/run/secrets/kubernetes.io/serviceaccount from kube-api-access-wzhhs (ro)
  traffic-agent:
    Container ID:  containerd://ef3605a60f7c02229f156e3dc0e99f9b055fba1037587513871e64180670d0a4
    Image:         docker.io/datawire/ambassador-telepresence-agent:1.12.6
    Image ID:      docker.io/datawire/ambassador-telepresence-agent@sha256:2652d2767d1e8968be3fb22f365747315e25ac95e12c3d39f1206080a1e66af3
    Port:          9900/TCP
    Host Port:     0/TCP
    Args:
      agent
    State:          Running
      Started:      Thu, 16 Jun 2022 11:38:41 +0200
    Ready:          True
    Restart Count:  0
    Readiness:      exec [/bin/stat /tmp/agent/ready] delay=0s timeout=1s period=10s #success=1 #failure=3
    Environment:
      _TEL_AGENT_POD_IP:   (v1:status.podIP)
      _TEL_AGENT_NAME:    hello-774455b6f5-6x6vs (v1:metadata.name)
    Mounts:
      /etc/traffic-agent from traffic-config (rw)
      /tel_app_exports from export-volume (rw)
      /tel_pod_info from traffic-annotations (rw)
      /var/run/secrets/kubernetes.io/serviceaccount from kube-api-access-wzhhs (ro)
Conditions:
  Type              Status
  Initialized       True 
  Ready             True 
  ContainersReady   True 
  PodScheduled      True 
Volumes:
  kube-api-access-wzhhs:
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
    Type:        EmptyDir (a temporary directory that shares a pod's lifetime)
    Medium:      
    SizeLimit:   <unset>
QoS Class:       BestEffort
Node-Selectors:  <none>
Tolerations:     node.kubernetes.io/not-ready:NoExecute op=Exists for 300s
                 node.kubernetes.io/unreachable:NoExecute op=Exists for 300s
Events:
  Type    Reason     Age   From               Message
  ----    ------     ----  ----               -------
  Normal  Scheduled  13m   default-scheduler  Successfully assigned default/hello-774455b6f5-6x6vs to multi
  Normal  Pulling    13m   kubelet            Pulling image "docker.io/datawire/ambassador-telepresence-agent:1.12.6"
  Normal  Pulled     13m   kubelet            Successfully pulled image "docker.io/datawire/ambassador-telepresence-agent:1.12.6" in 17.043659509s
  Normal  Created    13m   kubelet            Created container tel-agent-init
  Normal  Started    13m   kubelet            Started container tel-agent-init
  Normal  Pulled     13m   kubelet            Container image "registry.k8s.io/echoserver:1.4" already present on machine
  Normal  Created    13m   kubelet            Created container echoserver
  Normal  Started    13m   kubelet            Started container echoserver
  Normal  Pulled     13m   kubelet            Container image "docker.io/datawire/ambassador-telepresence-agent:1.12.6" already present on machine
  Normal  Created    13m   kubelet            Created container traffic-agent
  Normal  Started    13m   kubelet            Started container traffic-agent
```

### Uninstalling

You can uninstall the traffic-agent from specific deployments or from all deployments. Or you can choose to uninstall everything in which
case the traffic-manager and all traffic-agents will be uninstalled.

```
$ telepresence helm uninstall
```
will remove everything that was automatically installed by telepresence from the cluster.

### Troubleshooting

The telepresence background processes `daemon` and `connector` both produces log files that can be very helpful when problems are
encountered. The files are named `daemon.log` and `connector.log`. The location of the logs differ depending on what platform that is used:

- macOS `~/Library/Logs/telepresence`
- Linux `~/.cache/telepresence/logs`
- Windows `"%USERPROFILE%\AppData\Local\logs"`

Visit the troubleshooting section in the Telepresence documentation for more advice:  
[Troubleshooting](https://www.getambassador.io/docs/telepresence/latest/troubleshooting/)
