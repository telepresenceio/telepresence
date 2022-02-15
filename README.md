# Telepresence 2: fast, efficient local development for Kubernetes microservices

[<img src="https://cncf-branding.netlify.app/img/projects/telepresence/horizontal/color/telepresence-horizontal-color.png" width="80"/>](https://cncf-branding.netlify.app/img/projects/telepresence/horizontal/color/telepresence-horizontal-color.png)

Telepresence gives developers infinite scale development environments for Kubernetes. 

Website: [https://www.telepresence.io](https://www.telepresence.io)  
Slack: [Discuss](https://datawire-oss.slack.com/signup#/domain-signup)

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
$ kubectl create deploy hello --image=k8s.gcr.io/echoserver:1.4
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
Client v2.0.2
```

### Establish a connection to  the cluster (outbound traffic)

Let telepresence connect:
```console
$ telepresence connect
Launching Telepresence Daemon v2.0.2 (api v3)
Connecting to traffic manager...
Connected to context default (https://35.232.104.64)
```

A session is now active and outbound connections will be routed to the cluster. I.e. your laptop is "inside" the cluster.

```console
$ curl hello.default
CLIENT VALUES:
client_address=10.42.0.7
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
user-agent=curl/7.71.1
BODY:
-no body in request-
```

### Intercept the service. I.e. redirect traffic to it to our laptop (inbound traffic)

Add an intercept for the hello deployment on port 9000. Here, we also start a service listening on that port:

```console
$ telepresence intercept hello --port 9000 -- python3 -m http.server 9000
Using deployment hello
intercepted
    State       : ACTIVE
    Destination : 127.0.0.1:9000
    Intercepting: all connections
Serving HTTP on :: port 9000 (http://[::]:9000/) ...
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
::ffff:127.0.0.1 - - [17/Feb/2021 13:14:20] "GET / HTTP/1.1" 200 -
::ffff:127.0.0.1 - - [17/Feb/2021 13:16:54] "GET / HTTP/1.1" 200 -
```

Since telepresence is now intercepting services in the default namespace, all services in that namespace can now be reached directly by their name. You can of course still use the namespaced name too, e.g. `curl hello.default`.

### Clean-up and close daemon processes

End the service with `<ctrl>-C` and then try `curl hello.default` or `http://hello.default` again. The intercept is gone, and the echo service responds as normal. Using just `curl hello` will no longer succeed. This is because telepresence stopped mapping the default namespace when there were no more intercepts using it.

Now end the session too. Your desktop no longer has access to the cluster internals.
```console
$ telepresence quit -u
Telepresence Network is already disconnected
Telepresence Traffic Manager had already quit
$ telepresence quit -r
Telepresence Network quitting...done
Telepresence Traffic Manager is already disconnected
$ curl hello.default
curl: (6) Could not resolve host: hello.default
```

### Start outbound and intercept with one single command

There is no need to start a telepresence subshell when doing an intercept. Telepresence will automatically detect that a session is active, and if not, start one. The session then ends when the command exits, as shown in this example:

```console
telepresence intercept hello --port 9000 -- python3 -m http.server 9000
Launching Telepresence Daemon v2.0.1-64-g814052e (api v3)
Connecting to traffic manager...
Connected to context default (https://35.202.114.63)
Using deployment hello
intercepted
    State       : ACTIVE
    Destination : 127.0.0.1:9000
    Intercepting: all connections
Serving HTTP on :: port 9000 (http://[::]:9000/) ...
::ffff:127.0.0.1 - - [17/Feb/2021 14:05:37] "GET / HTTP/1.1" 200 -
^C
Keyboard interrupt received, exiting.
Disconnecting...done
Telepresence Daemon quitting...done
```

### What got installed in the cluster?

At first glance, we can see that the deployment is installed ...
```console
kubectl get svc,deploy,pod
NAME                 TYPE        CLUSTER-IP     EXTERNAL-IP   PORT(S)   AGE
service/kubernetes   ClusterIP   10.43.0.1      <none>        443/TCP   25m
service/hello        ClusterIP   10.43.73.112   <none>        80/TCP    23m

NAME                    READY   UP-TO-DATE   AVAILABLE   AGE
deployment.apps/hello   1/1     1            1           23m

NAME                        READY   STATUS    RESTARTS   AGE
pod/hello-75c8ffd99-dklkl   2/2     Running   0          15m
```

... and that the traffic-manager is installed in the "ambassador" namespace.
```console
kubectl -n ambassador get svc,deploy,pod
NAME                      TYPE        CLUSTER-IP   EXTERNAL-IP   PORT(S)             AGE
service/traffic-manager   ClusterIP   None         <none>        8022/TCP,8081/TCP   23m

NAME                              READY   UP-TO-DATE   AVAILABLE   AGE
deployment.apps/traffic-manager   1/1     1            1           23m

NAME                                   READY   STATUS    RESTARTS   AGE
pod/traffic-manager-596b6cdf68-sclsx   1/1     Running   0          20m
```

The traffic-agent is installed too, in the hello pod.

```console
kubectl describe pod hello-75c8ffd99-dklkl
Name:         hello-75c8ffd99-dklkl
Namespace:    default
Priority:     0
Node:         bobtester/10.88.24.2
Start Time:   Wed, 17 Feb 2021 13:13:03 +0100
Labels:       app=hello
              pod-template-hash=75c8ffd99
Annotations:  <none>
Status:       Running
IP:           10.42.0.8
IPs:
  IP:           10.42.0.8
Controlled By:  ReplicaSet/hello-75c8ffd99
Containers:
  echoserver:
    Container ID:   containerd://270098cea9f15fc8974603bde47fde7d36022524967d7b40a81f18324c657686
    Image:          k8s.gcr.io/echoserver:1.4
    Image ID:       sha256:523cad1a4df732d41406c9de49f932cd60d56ffd50619158a2977fd1066028f9
    Port:           <none>
    Host Port:      <none>
    State:          Running
      Started:      Wed, 17 Feb 2021 13:13:03 +0100
    Ready:          True
    Restart Count:  0
    Environment:    <none>
    Mounts:
      /var/run/secrets/kubernetes.io/serviceaccount from default-token-zkwqq (ro)
  traffic-agent:
    Container ID:  containerd://b0253a0e3ecc3d03991d7e92c0ab92123fc60245cc0277a7a07185933690fc4a
    Image:         docker.io/datawire/tel2:2.0.2
    Image ID:      docker.io/datawire/tel2@sha256:9002068a5dc224c029754c80e1b4616139a8f4aca5608942f75488debbe387cf
    Port:          9900/TCP
    Host Port:     0/TCP
    Args:
      agent
    State:          Running
      Started:      Wed, 17 Feb 2021 13:13:04 +0100
    Ready:          True
    Restart Count:  0
    Environment:
      TELEPRESENCE_CONTAINER:  echoserver
      LOG_LEVEL:               debug
      AGENT_NAME:              hello
      AGENT_POD_NAME:          hello-75c8ffd99-dklkl (v1:metadata.name)
      AGENT_NAMESPACE:         default (v1:metadata.namespace)
      APP_PORT:                8080
    Mounts:
      /var/run/secrets/kubernetes.io/serviceaccount from default-token-zkwqq (ro)
Conditions:
  Type              Status
  Initialized       True
  Ready             True
  ContainersReady   True
  PodScheduled      True
Volumes:
  default-token-zkwqq:
    Type:        Secret (a volume populated by a Secret)
    SecretName:  default-token-zkwqq
    Optional:    false
QoS Class:       BestEffort
Node-Selectors:  <none>
Tolerations:     node.kubernetes.io/not-ready:NoExecute op=Exists for 300s
                 node.kubernetes.io/unreachable:NoExecute op=Exists for 300s
Events:
  Type    Reason     Age   From               Message
  ----    ------     ----  ----               -------
  Normal  Scheduled  19m   default-scheduler  Successfully assigned default/hello-75c8ffd99-dklkl to bobtester
  Normal  Pulled     19m   kubelet            Container image "k8s.gcr.io/echoserver:1.4" already present on machine
  Normal  Created    19m   kubelet            Created container echoserver
  Normal  Started    19m   kubelet            Started container echoserver
  Normal  Pulled     19m   kubelet            Container image "docker.io/datawire/tel2:2.0.2" already present on machine
  Normal  Created    19m   kubelet            Created container traffic-agent
  Normal  Started    19m   kubelet            Started container traffic-agent
```

### Uninstalling

You can uninstall the traffic-agent from specific deployments or from all deployments. Or you can choose to uninstall everything in which case the traffic-manager and all traffic-agents will be uninstalled.

```
telepresence uninstall --everything
```
will remove everything that was automatically installed by telepresence from the cluster.

### Troubleshooting

The telepresence background processes `daemon` and `connector` both produces log files that can be very helpful when problems are encountered. The files are named `daemon.log` and `connector.log`. The location of the logs differ depending on what platform that is used:

- macOS `~/Library/Logs/telepresence`
- Linux `~/.cache/telepresence/logs`

## Comparison to classic Telepresence

Telepresence will launch your command, or a shell, when you start a session. When that program ends, the session ends and Telepresence cleans up.

### What works

- Outbound: You can `curl` a service running in the cluster while a session is running.
- Inbound: You can intercept a deployment, causing all requests to that deployment to go to your laptop instead.
- Namespaces: You can intercept multiple deployments in different namespaces simultaneously.
- Environment variables: The environment variables of the intercepted pod can be captured in a file or propagated to a command.
- Filesystem forwarding for volume mounts: If the intercepted service has mounted volumes, those are made available as remote mounts on the desktop during an intercept.
- Also Proxy: If you have a resource that is external to the cluster that is needed for your intercept, you can create a Headless Service (including ExternalName) that points to your resource to access it from your local machine.

### What doesn't work yet

- Container method

### What behaves differently

Telepresence installs the Traffic Manager in your cluster if it is not already present. This deployment remains unless you uninstall it.

Telepresence installs the Traffic Agent as an additional container in any deployment you intercept, and modifies any associated services it finds to route traffic through the agent. This modification persists unless you uninstall it.

You can launch other Telepresence sessions to the same cluster while an existing session is running, letting you intercept other deployments.
