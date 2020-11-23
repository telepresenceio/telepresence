# Telepresence 2

This is internal to Ambassador Labs.

## Walkthrough

```console
$ # Start with an empty cluster

$ kubectl create deploy hello --image=ark3/hello-world >& /dev/null

$ kubectl expose deploy hello --port 80 --target-port 8000 >& /dev/null

$ kubectl get ns,svc,deploy,po
NAME                        STATUS   AGE
namespace/default           Active   24h
namespace/kube-system       Active   24h
namespace/kube-public       Active   24h
namespace/kube-node-lease   Active   24h

NAME                 TYPE        CLUSTER-IP     EXTERNAL-IP   PORT(S)   AGE
service/kubernetes   ClusterIP   10.43.0.1      <none>        443/TCP   24h
service/hello        ClusterIP   10.43.10.250   <none>        80/TCP    4s

NAME                    READY   UP-TO-DATE   AVAILABLE   AGE
deployment.apps/hello   1/1     1            1           10s

NAME                         READY   STATUS    RESTARTS   AGE
pod/hello-84bcfd479f-8pkfc   1/1     Running   0          10s

$ telepresence --version
Client v0.2.0 (api v3)

$ telepresence
Launching Telepresence Daemon v0.2.0 (api v3)
Connecting to traffic manager...
Connected to context default (https://35.232.104.64)
Starting a /bin/bash subshell

$ # Now outbound works

$ curl -v hello
*   Trying 10.43.10.250:80...
* Connected to hello (10.43.10.250) port 80 (#0)
> GET / HTTP/1.1
> Host: hello
> User-Agent: curl/7.73.0
> Accept: */*
> 
* Mark bundle as not supporting multiuse
* HTTP 1.0, assume close after body
< HTTP/1.0 200 OK
< Content-Type: text/html; charset=utf-8
< Content-Length: 14
< Server: Werkzeug/0.15.2 Python/3.7.3
< Date: Thu, 19 Nov 2020 15:48:18 GMT
< 
Hello, world!
* Closing connection 0

$ # Add an intercept-friendly service

$ # We will support intercepting hello soon

$ kubectl apply -f k8s/echo-easy.yaml 
service/echo-easy created
deployment.apps/echo-easy created

$ curl echo-easy
Request served by echo-easy-fc656dc5d-dhqzb

HTTP/1.1 GET /

Host: echo-easy
User-Agent: curl/7.73.0
Accept: */*

$ # Intercept it

$ telepresence --intercept echo-easy --port 9000

FIXME

$ curl echo-easy

FIXME: Should be 52 empty reply

$ # Now run something to answer those requests

$ python3 -m http.server 9000 &

FIXME

$ curl echo-easy

FIXME: Should be a directory listing

$ exit  # stop the intercept

FIXME: Should be cleanup message

$ curl echo-easy

FIXME: Should be echo as above

$ exit

FIXME: Should be cleanup message
```

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

You can launch other Telepresence sessions to the same cluster while an existing session is running, letting you intercept other deployments. When doing so, it is important to end the first session last because it established the traffic-manager connection and will close it when it ends, rendering the other services disconnected.

