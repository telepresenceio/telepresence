# Telepresence RESTful API server

Telepresence can run a RESTful API server on the local host, both on the local workstation and in a pod that contains a `traffic-agent`. The server currently has three endpoints. The standard `healthz` endpoint, the `consume-here` endpoint, and the `intercept-info` endpoint.

## Enabling the server
The server is enabled by setting the `telepresenceAPI.port` to a valid port number in the [Telepresence Helm Chart](https://github.com/telepresenceio/telepresence/tree/release/v2/charts/telepresence). The value may be passed explicitly to Helm during the installation.

## Querying the server
On the cluster's side, it's the `traffic-agent` of potentially intercepted pods that runs the server. The server can be accessed using `http://<TELEPRESENCE_API_HOST>:<TELEPRESENCE_API_PORT>/<some endpoint>` from the application container. Telepresence ensures that the container has the `TELEPRESENCE_API_HOST` and `TELEPRESENCE_API_PORT` environment variable set when the `traffic-agent` is installed. On the workstation, it is the `user-daemon` that runs the server. It uses the `TELEPRESENCE_API_PORT` that is conveyed in the environment of the intercept. This means that the server can be accessed the exact same way locally if the environment is propagated correctly to the interceptor process.

## Endpoints

The `consume-here` and `intercept-info` endpoints are both intended to be queried with an optional path query and a set of headers, typically obtained from a Kafka message or similar. Telepresence provides the ID of the intercept in the environment variable `TELEPRESENCE_INTERCEPT_ID` during an intercept. This ID must be provided in a `x-telepresence-caller-intercept-id: = <ID>` header. Telepresence needs this to identify the caller correctly. The `<TELEPRESENCE_INTERCEPT_ID>` will be empty when running in the cluster, but it's harmless to provide it there too, so there's no need for conditional code.

There are three prerequisites to fulfill before testing The `consume-here` and `intercept-info` endpoints using `curl -v` on the workstation:
1. An intercept must be active
2. The "/healthz" endpoint must respond with OK
3. The ID of the intercept must be known. It will be visible as `ID` in the output of `telepresence list --debug`.

### healthz
The `http://localhost:<TELEPRESENCE_API_PORT>/healthz` endpoint should respond with status code 200 OK. If it doesn't, then something isn't configured correctly. Check that the `traffic-agent` container is present and that the `TELEPRESENCE_API_PORT` has been added to the environment of the application container and/or in the environment that is propagated to the interceptor that runs on the local workstation.

#### test endpoint using curl
A `curl -v` call can be used to test the endpoint when an intercept is active. This example assumes that the API port is configured to be 9980.
```console
$ curl -v localhost:9980/healthz
* Host localhost:9980 was resolved.
* IPv6: ::1
* IPv4: 127.0.0.1
*   Trying [::1]:9980...
* Connected to localhost (::1) port 9980
* using HTTP/1.x
> GET /healthz HTTP/1.1
> Host: localhost:9980
> User-Agent: curl/8.11.1
> Accept: */*
> 
* Request completely sent off
< HTTP/1.1 200 OK
< Date: Fri, 03 Oct 2025 05:39:47 GMT
< Content-Length: 0
< 
* Connection #0 to host localhost left intact
```

### consume-here
`http://<TELEPRESENCE_API_HOST>:<TELEPRESENCE_API_PORT>/consume-here` will respond with "true" (consume the message) or "false" (leave the message on the queue). When running in the cluster, this endpoint will respond with `false` if the headers match an ongoing intercept for the same workload because it's assumed that it's up to the intercept to consume the message. When running locally, the response is inverted. Matching headers means that the message should be consumed.

#### test endpoint using curl
Assuming that the API-server runs on port 9980, that the intercept was started with `--http-header x=y`, we can now check that the "/consume-here" returns "true" for given headers. Use `telepresence list --debug` to find the ID of the intercept.

```console
$ curl -v localhost:9980/consume-here -H 'x-telepresence-caller-intercept-id:9dbb0afa-38ec-48e2-a975-e664e579e197:apitest' -H 'x:y'
* Host localhost:9980 was resolved.
* IPv6: ::1
* IPv4: 127.0.0.1
*   Trying [::1]:9980...
* Connected to localhost (::1) port 9980
* using HTTP/1.x
> GET /consume-here HTTP/1.1
> Host: localhost:9980
> User-Agent: curl/8.11.1
> Accept: */*
> x-telepresence-caller-intercept-id:9dbb0afa-38ec-48e2-a975-e664e579e197:apitest
> x:y
> 
* Request completely sent off
< HTTP/1.1 200 OK
< Content-Type: application/json
< Date: Fri, 03 Oct 2025 05:45:16 GMT
< Content-Length: 4
< 
* Connection #0 to host localhost left intact
true
```

If you can run curl from the pod, you can try the exact same URL. The result should be "false" when there's an ongoing intercept. The `x-telepresence-caller-intercept-id` is not needed when the call is made from the pod.

### intercept-info
`http://<TELEPRESENCE_API_HOST>:<TELEPRESENCE_API_PORT>/intercept-info` is intended to be queried with an optional path query and a set of headers, typically obtained from a Kafka message or similar, and will respond with a JSON structure containing the two booleans `clientSide` and `intercepted`, and a `metadata` map which corresponds to the `--metadata` key pairs used when the intercept was created. This field is always omitted in case `intercepted` is `false`.

#### test endpoint using curl
Assuming that the API-server runs on port 9980, that the intercept was started with `--http-header x=y --metadata a=b --metadata b=c`, we can now check that the "/intercept-info" returns information for the given path and headers.
```console
$ curl -v localhost:9980/intercept-info -H 'x-telepresence-caller-intercept-id:9dbb0afa-38ec-48e2-a975-e664e579e197:apitest' -H 'x:y'
* Host localhost:9980 was resolved.
* IPv6: ::1
* IPv4: 127.0.0.1
*   Trying [::1]:9980...
* Connected to localhost (::1) port 9980
* using HTTP/1.x
> GET /intercept-info HTTP/1.1
> Host: localhost:9980
> User-Agent: curl/8.11.1
> Accept: */*
> x-telepresence-caller-intercept-id:9dbb0afa-38ec-48e2-a975-e664e579e197:echo-easy
> x:y
> 
* Request completely sent off
< HTTP/1.1 200 OK
< Content-Type: application/json
< Date: Fri, 03 Oct 2025 05:48:35 GMT
< Content-Length: 38
< 
* Connection #0 to host localhost left intact
{"intercepted":true,"clientSide":true},"metadata":{"a":"b","b":"c"}}
```
