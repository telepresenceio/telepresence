# Telepresence RESTful API server

Telepresence can run a RESTful API server on the local host, both on the local workstation and in a pod that contains a `traffic-agent`. The server currently has two endpoints. The standard `healthz` endpoint and the `consume-here` endpoint.

## Enabling the server
The server is enabled by setting the `telepresenceAPI.port` to a valid port number in the [Telepresence Helm Chart](https://github.com/telepresenceio/telepresence/tree/release/v2/charts/telepresence). The values may be passed  explicitly to Helm during install, or configured using the [Telepresence Config](../config#restful-api-server) to impact an auto-install.

## Querying the server
On the cluster's side, it's the `traffic-agent` of potentially intercepted pods that runs the server. The server can be accessed using `http://localhost:<TELEPRESENCE_API_PORT>/<some endpoint>` from the application container. Telepresence ensures that the container has the `TELEPRESENCE_API_PORT` environment variable set when the `traffic-agent` is installed. On the workstation, it is the `user-daemon` that runs the server. It uses the `TELEPRESENCE_API_PORT` that is conveyed in the environment of the intercept. This means that the server can be accessed the exact same way locally, provided that the environment is propagated correctly to the interceptor process.

## Endpoints

The `consume-here` and `intercept-info` endpoints are both intended to be queried with an optional path query and a set of headers, typically obtained from a Kafka message or similar. Telepresence provides the ID of the intercept in the environment variable [TELEPRESENCE_INTERCEPT_ID](../environment/#telepresence_intercept_id) during an intercept. This ID must be provided in a `x-telepresence-caller-intercept-id: = <ID>` header. Telepresence needs this to identify the caller correctly. The `<TELEPRESENCE_INTERCEPT_ID>` will be empty when running in the cluster, but it's harmless to provide it there too, so there's no need for conditional code.

There are three prerequisites to fulfill before testing The `consume-here` and `intercept-info` endpoints using `curl -v` on the workstation:
1. An intercept must be active
2. The "/healthz" endpoint must respond with OK
3. The ID of the intercept must be known. It will be visible as `ID` in the output of `telepresence list --debug`.

### healthz
The `http://localhost:<TELEPRESENCE_API_PORT>/healthz` endpoint should respond with status code 200 OK. If it doesn't then something isn't configured correctly. Check that the `traffic-agent` container is present and that the `TELEPRESENCE_API_PORT` has been added to the environment of the application container and/or in the environment that is propagated to the interceptor that runs on the local workstation.

#### test endpoint using curl
A `curl -v` call can be used to test the endpoint when an intercept is active. This example assumes that the API port is configured to be 9980.
```console
$ curl -v localhost:9980/healthz
*   Trying ::1:9980...
* Connected to localhost (::1) port 9980 (#0)
> GET /healthz HTTP/1.1
> Host: localhost:9980
> User-Agent: curl/7.76.1
> Accept: */*
> 
* Mark bundle as not supporting multiuse
< HTTP/1.1 200 OK
< Date: Fri, 26 Nov 2021 07:06:18 GMT
< Content-Length: 0
< 
* Connection #0 to host localhost left intact
```

### consume-here
`http://localhost:<TELEPRESENCE_API_PORT>/consume-here` will respond with "true" (consume the message) or "false" (leave the message on the queue). When running in the cluster, this endpoint will respond with `false` if the headers match an ongoing intercept for the same workload because it's assumed that it's up to the intercept to consume the message. When running locally, the response is inverted. Matching headers means that the message should be consumed.

#### test endpoint using curl
Assuming that the API-server runs on port 9980, that the intercept was started with `--http-match x=y --http-path-prefix=/api`, we can now check that the "/consume-here" returns "true" for the path "/api" and given headers.
```console
$ curl -v localhost:9980/consume-here?path=/api -H 'x-telepresence-caller-intercept-id: 4392d394-100e-4f15-a89b-426012f10e05:apitest' -H 'x: y'
*   Trying ::1:9980...
* Connected to localhost (::1) port 9980 (#0)
> GET /consume-here?path=/api HTTP/1.1
> Host: localhost:9980
> User-Agent: curl/7.76.1
> Accept: */*
> x: y
> x-telepresence-caller-intercept-id: 4392d394-100e-4f15-a89b-426012f10e05:apitest
> 
* Mark bundle as not supporting multiuse
< HTTP/1.1 200 OK
< Content-Type: application/json
< Date: Fri, 26 Nov 2021 06:43:28 GMT
< Content-Length: 4
< 
* Connection #0 to host localhost left intact
true
```

If you can run curl from the pod, you can try the exact same URL. The result should be "false" when there's an ongoing intercept. The `x-telepresence-caller-intercept-id` is not needed when the call is made from the pod.

### intercept-info
`http://localhost:<TELEPRESENCE_API_PORT>/intercept-info` is intended to be queried with an optional path query and a set of headers, typically obtained from a Kafka message or similar, and will respond with a JSON structure containing the two booleans `clientSide` and `intercepted`, and a `metadata` map which corresponds to the `--http-meta` key pairs used when the intercept was created. This field is always omitted in case `intercepted` is `false`.

#### test endpoint using curl
Assuming that the API-server runs on port 9980, that the intercept was started with `--http-match x=y --http-path-prefix=/api --http-meta a=b --http-meta b=c`, we can now check that the "/intercept-info" returns information for the given path and headers.
```console
$ curl -v localhost:9980/intercept-info?path=/api -H 'x-telepresence-caller-intercept-id: 4392d394-100e-4f15-a89b-426012f10e05:apitest' -H 'x: y'
*   Trying ::1:9980...* Connected to localhost (127.0.0.1) port 9980 (#0)
> GET /intercept-info?path=/api HTTP/1.1
> Host: localhost:9980
> User-Agent: curl/7.79.1
> Accept: */*
> x: y
> x-telepresence-caller-intercept-id: 4392d394-100e-4f15-a89b-426012f10e05:apitest
>
* Mark bundle as not supporting multiuse
< HTTP/1.1 200 OK
< Content-Type: application/json
< Date: Tue, 01 Feb 2022 11:39:55 GMT
< Content-Length: 68
<
{"intercepted":true,"clientSide":true,"metadata":{"a":"b","b":"c"}}
* Connection #0 to host localhost left intact
```
