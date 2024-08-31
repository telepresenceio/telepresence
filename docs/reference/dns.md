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

  <!DOCTYPE html>
  <html>
  <head>
      <meta charset="UTF-8">
      <title>Emoji Vote</title>
  ...
```

Now we'll start an intercept against another service.

```
$ telepresence intercept web --port 8080

  Using Deployment web
  intercepted
      Intercept name    : web
      State             : ACTIVE
      Workload kind     : Deployment
      Destination       : 127.0.0.1:8080
      Volume Mount Point: /tmp/telfs-166119801
      Intercepting      : all TCP connections

$ curl webapp:80

  <!DOCTYPE html>
  <html>
  <head>
      <meta charset="UTF-8">
      <title>Emoji Vote</title>
  ...
```

The DNS resolver will also be able to resolve services using `<service-name>.<namespace>` regardless of what namespace the
client is connected to.

### Supported Query Types

The Telepresence DNS resolver is now capable of resolving queries of type `A`, `AAAA`, `CNAME`,
`MX`, `NS`, `PTR`, `SRV`, and `TXT`.

See [Outbound connectivity](../routing/#dns-resolution) for details on DNS lookups.
