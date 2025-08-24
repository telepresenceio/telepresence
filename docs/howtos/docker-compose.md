---
title: "Using Telepresence with Docker Compose"
hide_table_of_contents: true
---
# Telepresence Docker Compose Extensions

A Docker Compose file can contain extensions that Docker Compose ignores. The `telepresence compose` command functions similarly to `docker compose`, but will process any `x-tele` extensions present in the Docker Compose file or its overrides before passing the final Compose specification to Docker Compose.

The `x-tele` extensions are particularly useful when you have a set of services defined in a Docker Compose file that mirrors services running in a cluster, and you want your local services to interact with remote services or vice versa. The `x-tele` extensions enable your Compose services to either act as handlers when telepresence engages a remote service, or to temporarily act as proxies for remotely running services.

The extensions can be added directly to the `compose.yaml` file, or to a `compose.override.yaml` (merged automatically by Docker Compose).

## Supported `x-tele` Extensions

### Top-level Extension
The `x-tele` [top-level extension](../reference/compose#top-level-extension) is used to define a connection to the cluster, and to override the default mount behavior when engaging with remote services.

### Service Extensions

The `x-tele` [service extensions](../reference/compose#service-extensions) are used to define the behavior of a service when engaged with a remote service.

Telepresence supports the following types:

| Type                                        | Local service Behavior                                          | Similar to               |
|---------------------------------------------|-----------------------------------------------------------------|--------------------------|
| [connect](../reference/compose#connect)     | Service has access to the cluster's resources (DNS and routing) | `telepresence connect`   |
| [proxy](../reference/compose#proxy)         | Replaced with a proxy for a service in the cluster              | N/A                      |
| [wiretap](../reference/compose#wiretap)     | Receives wiretapped data from a service in the cluster          | `telepresence wiretap`   |
| [ingest](../reference/compose#ingest)       | Acts as the handler for an ingested container the cluster       | `telepresence ingest`    |
| [intercept](../reference/compose#intercept) | Acts as the handler for an intercepted service the cluster      | `telepresence intercept` |
| [replace](../reference/compose#replace)     | Replaces a remote container in the cluster                      | `telepresence replace`   |

All types imply a `connect`, and thus rely on the top-level `x-tele` extension that defines the connection to the cluster.

## Walkthrough and Samples
This documentation will give some examples on how to use the `x-tele` extension using the sample Emoji application, originally developed by Buoyant.io, from the https://github.com/telepresenceio/emojivoto repository. This app is easy to deploy locally using `docker compose up` or remotely to a cluster using `kubectl apply --kustomize`.

### Initial Steps

The examples assume that you have [installed](../install/manager.md) the Telepresence Traffic Manager in your cluster.

We start by ensuring that the Emojivoto application can be deployed, both locally using `docker compose up` and remotely in your cluster.

#### 1. Download the Emojivoto App

Clone the emojivoto git repository with the following command:
```console
$ git clone https://github.com/telepresenceio/emojivoto.git
```

#### 2. Use the app locally

```console
$ cd emojivoto
$ docker compose up
```

Now point your browser to http://localhost:8080/. The "Emoji Vote" page shows up. Try it out.

Tear down the local app
```console
$ docker compose down
```

#### 3. Use the app remotely

We Create the cluster resources by applying the `kustomize/deployment` directory using the following command:

```console
$ kubectl apply -k kustomize/deployment
namespace/emojivoto created
serviceaccount/emoji created
serviceaccount/voting created
serviceaccount/web created
service/emoji created
service/voting created
service/web created
deployment.apps/emoji created
deployment.apps/vote-bot created
deployment.apps/voting created
deployment.apps/web created
```

Check that the pods are up and running with:
```console
$ kubectl -n emojivoto get pod
NAME                       READY   STATUS    RESTARTS   AGE
emoji-7d8d6fb869-wp5kc     1/1     Running   0          23s
vote-bot-766b9f68b-9bsqk   1/1     Running   0          23s
voting-7d49b58d7b-n7bc8    1/1     Running   0          23s
web-7cc498695b-7dtcl       1/1     Running   0          23s
```

Connect to the cluster and verify that the web service is functional. The `telepresence serve web` will start the browser with a URL that points to the "web" service:
```console
$ telepresence connect -n emojivoto --docker
 ✔ Connected to context minikube, namespace emojivoto (https://192.168.49.2:8443)           0.8s
$ telepresence serve web
```

## Extend With "proxy"

Let's assume that we don't want to run the "voting" service locally. Instead, we want to replace it with a corresponding service that runs in the cluster. In other words, we want the local service to act as a _proxy_ for the remote service.

The original `compose.yaml` file contains this:

```yaml
services:
  web:
    image: ghcr.io/telepresenceio/emojivoto-web:0.3.0
    environment:
      - WEB_PORT=8080
      - EMOJISVC_HOST=emoji:8080
      - VOTINGSVC_HOST=voting:8080
      - INDEX_BUNDLE=dist/index_bundle.js
    ports:
      - "8080:8080"
    depends_on:
      - voting
      - emoji

  vote-bot:
    image: ghcr.io/telepresenceio/emojivoto-web:0.3.0
    entrypoint: emojivoto-vote-bot
    environment:
      - WEB_HOST=web:8080
    depends_on:
      - web

  emoji:
    image: ghcr.io/telepresenceio/emojivoto-emoji:0.3.0
    environment:
      - GRPC_PORT=8080
    ports:
      - "8081:8080"

  voting:
    image: ghcr.io/telepresenceio/emojivoto-voting:0.3.0
    environment:
      - GRPC_PORT=8080
      - POLL_FILE=/data/polls.json
    ports:
      - "8082:8080"
    volumes:
      - data:/data

volumes:
  data:
```

The proxy requires a Telepresence connection to the cluster, using the `emojivoto` namespace in this example. Connections are defined in a top-level `x-tele` extension, where each connection is identified by a name and configured with attributes that correspond to the flags used in the `telepresence connect` command. The top-level extension is structured as follows:

```yaml
x-tele:
  connections:
    - name: emojivoto
      namespace: emojivoto
```

To enable proxy functionality, an extension of type `proxy` is added to the `voting` service declaration:

```yaml
x-tele:
  type: proxy
  connection: emojivoto
```

The `connection: emojivoto` field specifies that the proxy uses the `emojivoto` connection defined in the top-level extension, linking it to the `emojivoto` namespace. This field is optional when the top-level extension includes only one connection. Similarly, the `name: emojivoto` in the connection declaration is optional and only required when multiple connections are defined.

The resulting file will then look like this:

```yaml
x-tele:
  connections:
    - name: emojivoto
      namespace: emojivoto
services:
  web:
    image: ghcr.io/telepresenceio/emojivoto-web:0.3.0
    environment:
      - WEB_PORT=8080
      - EMOJISVC_HOST=emoji:8080
      - VOTINGSVC_HOST=voting:8080
      - INDEX_BUNDLE=dist/index_bundle.js
    ports:
      - "8080:8080"
    depends_on:
      - voting
      - emoji

  vote-bot:
    image: ghcr.io/telepresenceio/emojivoto-web:0.3.0
    entrypoint: emojivoto-vote-bot
    environment:
      - WEB_HOST=web:8080
    depends_on:
      - web

  emoji:
    image: ghcr.io/telepresenceio/emojivoto-emoji:0.3.0
    environment:
      - GRPC_PORT=8080
    ports:
      - "8081:8080"

  voting:
    x-tele:
      type: proxy
      connection: emojivoto
    image: ghcr.io/telepresenceio/emojivoto-voting:0.3.0
    environment:
      - GRPC_PORT=8080
      - POLL_FILE=/data/polls.json
    ports:
      - "8082:8080"
    volumes:
      - data:/data

volumes:
  data:
```

> [!TIP]
> Instead of modifying the original `compose.yaml` file, we can add a new file adjacent to it and call it `compose.override.yaml`. Docker Compose will automatically merge this override with the `compose.yaml`. So, leave original `compose.yaml` intact, and instead add a `compose.override.yaml` file with the following contents (the optional connection name and proxy connection reference are both removed):
>
> ```yaml
> x-tele:
>   connections:
>     - namespace: emojivoto
> services:
>  voting:
>    x-tele:
>      type: proxy
> ```

### Running the Extended Sample

Running with `telepresence compose up` will discover the extension, connect to the cluster, modify an in-memory version of the Compose specification so that it no longer contains the "voting" service, alter the DNS so that lookups for this service instead find the one in the cluster, and configure routing so that the "web" service still finds the "voting" service. We can verify this using:
```console
$ telepresence compose
 ✔ Connected to context minikube, namespace emojivoto (https://192.168.49.2:8443)     2.4s 
 ✔ Proxied service voting                                                             0.0s 
[+] Running 4/4
 ✔ Network emojivoto_default        Created                                           0.1s 
 ✔ Container emojivoto-emoji-1  Created                                               0.0s 
 ✔ Container emojivoto-web-1        Created                                           0.0s 
 ✔ Container emojivoto-vote-bot-1   Created                                           0.0s 
Attaching to emoji-1, vote-bot-1, web-1
emoji-1     | 2025/07/19 05:42:39 Starting grpc server on GRPC_PORT=[8080]
web-1       | 2025/07/19 05:42:39 Connecting to [voting:8080]
web-1       | 2025/07/19 05:42:39 Connecting to [emoji:8080]
web-1       | 2025/07/19 05:42:39 Starting web server on WEB_PORT=[8080] and MESSAGE_OF_THE_DAY=[]
vote-bot-1  | ✔ Voting for :older_man:
vote-bot-1  | ✔ Voting for :100:
vote-bot-1  | ✔ Voting for :bulb:
...
```
We now see "Proxied service voting" and then, in contrast to the output from a `docker compose up`, no further output from that service. The `vote-bot-1` continues to vote though, to it's obviously still talking to a `voting`.

### Takeaways
Using our Telepresence "proxy" extension, we have now successfully modified our setup so that the services in the compose.yaml file interact with a service in the cluster.

### Proxy the web

Can we proxy the web service and still reach it using `localhost:8080` in our browser? Let's give it a try using the following `compose.override.yaml` file:

```yaml
x-tele:
  connections:
    - namespace: emojivoto
services:
  web:
    x-tele:
      type: proxy
      ports:
        - 8080:80
```

Worth noting here is that the original docker-compose service will expose port 8080, so that's what our proxy must expose to other containers. In the cluster, however, the web service uses port 80. This is why we need to specify the port mapping in the extension:
```yaml
      ports:
        - 8080:80
```

With this change, we can now run the sample using `telepresence compose up` and connect to the web service using `localhost:8080` in our browser.

## Extend With "replace"

Our previous example used a proxy to replace a local service with a remote service. In this sample we will do the opposite. We will make the remote services talk to services in our Docker Compose file. In essence, we will let the remote `web` and `vote-bot` service use the `emoji` and `vote` service that we run locally.

Our extensions look like this:
```yaml
x-tele:
  connections:
    - namespace: emojivoto
services:
  emoji:
    x-tele:
      type: replace
  voting:
    x-tele:
      type: replace
  vote-bot:
    profiles:
      - notEnabled
```

> [!NOTE]
> The last part:
> ```yaml
>   vote-bot:
>     profiles:
>       - notEnabled
> ```
> effectively disables the local `vote-bot` service so that only the vote-bot running in the cluster is active. It's optional, but it makes it easier to see what happens when we run the sample:

```console
$ telepresence compose up
 ✔ Connected to context minikube, namespace emojivoto (https://192.168.49.2:8443)     2.8s 
[+] Engaging 2/2
 ✔ emoji  Replaced service emoji                                                      2.0s 
 ✔ voting Replaced service voting                                                     1.6s 
[+] Running 4/4
 ✔ Network emojivoto_default    Created                                               0.0s 
 ✔ Container emojivoto-voting1  Created                                               0.1s 
 ✔ Container emojivoto-emoji-1  Created                                               0.1s 
 ✔ Container emojivoto-web-1    Created                                               0.1s 
Attaching to emoji-1, voting-1, web-1
emoji-1   | 2025/08/05 09:17:39 Starting prom metrics on PROM_PORT=[8801]
emoji-1   | 2025/08/05 09:17:39 Starting grpc server on GRPC_PORT=[8080]
voting-1  | 2025/08/05 09:17:39 Storing votes in file /data/polls.json
voting-1  | 2025/08/05 09:17:39 Starting prom metrics on PROM_PORT=[8801]
voting-1  | 2025/08/05 09:17:39 Starting grpc server on GRPC_PORT=[8080]
voting-1  | 2025/08/05 09:17:39 Using failureRate [0.000000] and artificialDelayDuration [0s]
web-1     | 2025/08/05 09:17:39 Connecting to [voting:8080]
web-1     | 2025/08/05 09:17:39 Connecting to [emoji:8080]
web-1     | 2025/08/05 09:17:39 Starting web server on WEB_PORT=[8080] and MESSAGE_OF_THE_DAY=[]
voting-1  | 2025/07/20 04:39:58 Voted for [:fax:], which now has a total of [12] votes
voting-1  | 2025/07/20 04:39:59 Voted for [:doughnut:], which now has a total of [231] votes
voting-1  | 2025/07/20 04:40:00 Voted for [:flight_departure:], which now has a total of [8] votes
```

We can observe that after an initial delay - caused by the remote vote bot reconnecting after the replacement of the vote container - the votes arrive, even though no vote-bot is running locally. Furthermore, if we start a browser on http://localhost:8080 now, we see the same leaderboard as a browser started using `telepresence serve web` which serves up the remote service.

In the cluster, the pods for the "voting" and "emoji" deployments have been replaced with traffic-agents that redirect all traffic to their corresponding "voting" and "emoji" Docker Compose service. We can easily verify this using:
```console
$ kubectl -n emojivoto get pod -l app=voting -o jsonpath='{.items.*.spec.containers.*.name}'
traffic-agent
$ kubectl -n emojivoto get pod -l app=emoji -o jsonpath='{.items.*.spec.containers.*.name}'
traffic-agent
```

### Remote Mounts

One interesting observation is that the vote counts don't start from zero. Instead, they are synced with the vote counts used by the voting service in the cluster. This is because the "replace" extension automatically replaced the mounted "data" volume with a remote mount of the corresponding volume in the replaced container. This default behavior can be controlled using mount policies.

#### Preventing Remote Mounts

To prevent the remote mounts from happening, and instead keep the volumes created by Docker Compose, we can add a `mounts` object to the top-level `x-tele` extension in the `compose.override.yaml` file:
```yaml
x-tele:
  connections:
    - namespace: emojivoto
  mounts:
    - volume: data
      policy: local
```

The `mounts` object is a list of objects, each with a `volume` field that corresponds to the name of the volume in the Compose file or a `volumePattern`, a regular expression that matches that name, and a `policy` field that can be set to either `local`, `ignore`, `remote` or `remoteReadOnly`. The default is to use whatever policy that the traffic-agent uses for the volume.

### Takeaways
Using our Telepresence "replace" extension, we have successfully modified our setup so that multiple services in the cluster have been replaced by services that run locally as part of our Docker Compose spec.
