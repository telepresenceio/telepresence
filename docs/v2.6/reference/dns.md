# DNS resolution

The Telepresence DNS resolver is dynamically configured to resolve names using the namespaces of currently active intercepts. Processes running locally on the desktop will have network access to all services in the such namespaces by service-name only.

All intercepts contribute to the DNS resolver, even those that do not use the `--namespace=<value>` option. This is because `--namespace default` is implied, and in this context, `default` is treated just like any other namespace.

No namespaces are used by the DNS resolver (not even `default`) when no intercepts are active, which means that no service is available by `<svc-name>` only. Without an active intercept, the namespace qualified DNS name must be used (in the form `<svc-name>.<namespace>`).

See this demonstrated below, using the [quick start's](../../quick-start/) sample app services.

No intercepts are currently running, we'll connect to the cluster and list the services that can be intercepted.

```
$ telepresence connect

  Connecting to traffic manager...
  Connected to context default (https://<cluster-public-IP>)

$ telepresence list

  web-app-5d568ccc6b   : ready to intercept (traffic-agent not yet installed)
  emoji                : ready to intercept (traffic-agent not yet installed)
  web                  : ready to intercept (traffic-agent not yet installed)
  web-app-5d568ccc6b   : ready to intercept (traffic-agent not yet installed)

$ curl web-app:80

  curl: (6) Could not resolve host: web-app

```

This is expected as Telepresence cannot reach the service yet by short name without an active intercept in that namespace.

```
$ curl web-app.emojivoto:80

  <!DOCTYPE html>
  <html>
  <head>
      <meta charset="UTF-8">
      <title>Emoji Vote</title>
  ...
```

Using the namespaced qualified DNS name though does work.
Now we'll start an intercept against another service in the same namespace. Remember, `--namespace default` is implied since it is not specified.

```
$ telepresence intercept web --port 8080

  Using Deployment web
  intercepted
      Intercept name    : web
      State             : ACTIVE
      Workload kind     : Deployment
      Destination       : 127.0.0.1:8080
      Volume Mount Point: /tmp/telfs-166119801
      Intercepting      : HTTP requests that match all headers:
        'x-telepresence-intercept-id: 8eac04e3-bf24-4d62-b3ba-35297c16f5cd:web'

$ curl webapp:80

  <!DOCTYPE html>
  <html>
  <head>
      <meta charset="UTF-8">
      <title>Emoji Vote</title>
  ...
```

Now curling that service by its short name works and will as long as the intercept is active.

The DNS resolver will always be able to resolve services using `<service-name>.<namespace>` regardless of intercepts.

See [Outbound connectivity](../routing/#dns-resolution) for details on DNS lookups.
