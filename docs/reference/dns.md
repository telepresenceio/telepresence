---
title: DNS resolution
hide_table_of_contents: true
---
# DNS resolution

The Telepresence DNS resolver is dynamically configured to resolve names using the namespaces currently managed by the Traffic Manager. Processes running locally on the desktop will have network access to all services in the currently connected namespace by service-name only, and to other managed namespaces using service-name.namespace.

See this demonstrated below, using the [quick start's](../quick-start.md) sample app services.

We'll connect to a namespace in the cluster and list the services that can be intercepted.

```
$ telepresence connect --namespace default

  Connecting to traffic manager...
  Connected to context default, namespace default (https://<cluster-public-IP>)

$ telepresence list

  deployment web-app: ready to attach (traffic-agent not yet installed)
  deployment emoji  : ready to attach (traffic-agent not yet installed)
  deployment web    : ready to attach (traffic-agent not yet installed)

$ curl web-app:80

  <!DOCTYPE html>
  <html>
  <head>
      <meta charset="UTF-8">
      <title>Emoji Vote</title>
  ...
```

The DNS resolver will also be able to resolve services using `<service-name>.<namespace>` regardless of what namespace the
client is connected to as long as the given namespace is among the set managed by the Traffic Manager.

### Supported Query Types

The Telepresence DNS resolver is now capable of resolving queries of type `A`, `AAAA`, `CNAME`,
`MX`, `NS`, `PTR`, `SRV`, and `TXT`.

See [Outbound connectivity](routing.md#dns-resolution) for details on DNS lookups.
