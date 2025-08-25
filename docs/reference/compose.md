---
title: Telepresence Docker Compose Extension
hide_table_of_contents: true
---
# Telepresence Docker Compose Extension

The `x-tele` extension is added either at the top level or to a service. E.g.

```yaml
x-tele:
  connections:
    - namespace: <name>
services:
  some-name:
    x-tele:
      type: <extension type>
      ...
```

Those extensions are recognized by the `telepresence compose`, which acts as an extended `docker compose` command. Telepresence will create connections, engagements, and proxies based on the extensions and then modify the docker compose file with the necessary networks, mounts, and environment variables to make the extended services work.

## States

- `telepresence compose up` will ensure that the extended services are in the correct state.
- `telepresence compose create` is like `up`, but it will not start the containers, and therefore end any existing engagements once all the containers are created.
- `telepresence compose stop` ends the engagements, but it keeps telepresence connected, because the existing containers use the `teleroute` network backed by that connection.
- `telepresence compose down` will end the engagements, terminate the network, and quit telepresence.
- `telepresence config` will detect if the project is started, if so, produce the extended project file. Otherwise, it will produce the original project file's canonical form.
- `telepresence quit` will detect if a `telepresence compose` is running and, if so, issue a `telepresence compose down`.
## Top-level Extension

The Top-level extension describes the connection and mount configurations that are used by the service extensions:

| Name              | Description                                                                        | Type               | Default Value |
|-------------------|------------------------------------------------------------------------------------|--------------------|---------------|
| connections       | Connection configurations.                                                         | connection configs | empty         |
| mounts            | Mount configurations.                                                              | mount configs      | empty         |

### Connection Configuration

The `connections` field is a list of connection configurations. A single connection using the defaults from the current Kubernetes context is created when the list is empty.

Each connection configuration is an object with the following fields:

| Name              | Description                                                                        | Type    | Default Value                                                   |
|-------------------|------------------------------------------------------------------------------------|---------|-----------------------------------------------------------------|
| name              | The connection name                                                                | string  | generated based on the current Kubernetes context and namespace |
| namespace         | The namespace to connect to                                                        | string  | The namespace configured in the current Kubernetes context      |
| also-proxy        | Subnets that Telepresence should proxy in addition to the service and pod subnets. | CIDRs   | empty                                                           |
| never-proxy       | Subnets that Telepresence should never proxy.                                      | CIDRs   | empty                                                           |
| manager-namespace | The namespace in which the Telepresence Traffic Manager is running.                | string  | The name specified in the client config or "ambassador"         |
| mapped-namespaces | Namespaces that Telepresence should map as DNS domains.                            | strings | empty                                                           |

### Mount Configuration

The `mounts` field is a list of mount configurations that controls how the service extensions handle the volumes shared by the traffic-agent that the extended service will engage with. The mount configuration is an object with the following fields:

| Name          | Description                                                                                              | Type   | Default Value                   |
|---------------|----------------------------------------------------------------------------------------------------------|--------|---------------------------------|
| volume        | Name of a Docker Compose volume. Mutually exclusive to volumePattern.                                    | string | empty                           |
| volumePattern | Regular expression pattern matching one or several Docker Compose volumes. Mutually exclusive to volume. | string | empty                           |
| policy        | "local", "remote", or "remoteReadOnly"                                                                   | string | determined by the traffic-agent |

The mount policy determines how the volume is mounted by Docker Compose.
<dl>
<dt>local</dt>
<dd>The Docker Compose volume is not modified.</dd>
<dt>remote</dt>
<dd>The Docker Compose volume is modified to mount a remote volume without a read-only restriction. It might still be restricted by the remote volume's permissions.</dd>
<dt>remoteReadOnly</dt>
<dd>The Docker Compose volume is modified to mount a remote volume read-only restriction.</dd>
</dl>

## Service Extensions

The `x-tele` extension can be added to a Docker Compose service to extend the service's behavior. The extension must have a `type` field. Available types are:

| Type                    | Local service Behavior                                          | Similar to               |
|-------------------------|-----------------------------------------------------------------|--------------------------|
| [connect](#connect)     | Service has access to the cluster's resources (DNS and routing) | `telepresence connect`   |
| [proxy](#proxy)         | Replaced with a proxy for a service in the cluster              | N/A                      |
| [ingest](#ingest)       | Acts as the handler for an ingested container the cluster       | `telepresence ingest`    |
| [intercept](#intercept) | Acts as the handler for an intercepted service the cluster      | `telepresence intercept` |
| [replace](#replace)     | Replaces a remote container in the cluster                      | `telepresence replace`   |
| [wiretap](#wiretap)     | Receives wiretapped data from a service in the cluster          | `telepresence wiretap`   |

### connect

The `connect` extension can be used as is, but it is also the base for all the other extensions. It will ensure that the extended docker compose service has access to the cluster's network
and DNS by injecting a [teleroute](plugins.md#teleroute-network-plugin) network. The extension can have the following fields set:

| Name       | Description                                                           | Type   | Default Value |
|------------|-----------------------------------------------------------------------|--------|---------------|
| connection | The name of a connection declared in the top-level `x-tele` extension | string | empty         |

The `connection` field is required only when more than one connection is declared in the top-level `x-tele` extension.

### proxy

The `proxy` extension replaces the extended docker compose service with a proxy that redirects all traffic to a workload
in the cluster. The extension can have the following fields set:

| Name       | Description                                                           | Type    | Default Value                 |
|------------|-----------------------------------------------------------------------|---------|-------------------------------|
| connection | The name of a connection declared in the top-level `x-tele` extension | string  | empty                         |
| name       | Name of the proxied remote service                                    | string  | name of the compose service   |
| ports      | List of &lt;service-port&gt;:&lt;remote service-port&gt;> mappings    | strings | empty (route all ports as-is) |

### ingest

The `ingest` ensures that the docker compose service shares the environment and volumes of a remote container in the cluster. The extension can have the following fields set:

| Name       | Description                                                                   | Type    | Default Value               |
|------------|-------------------------------------------------------------------------------|---------|-----------------------------|
| connection | The name of a connection declared in the top-level `x-tele` extension         | string  | empty                       |
| name       | Name of the remote workload (typically the deployment)                        | string  | name of the compose service |
| container  | Name of the remote container                                                  | string  | empty                       |
| to-pod     | Ports to forward from the local compose service to the remote pod's localhost | strings | empty                       |

The `container` field is required when more than one container is declared in the remote workload.

### intercept

The `intercept` ensures that the docker compose service receives traffic from, and shares the environment and volumes of, a remote container in the cluster. The extension can have the following fields set:

| Name       | Description                                                                   | Type    | Default Value               |
|------------|-------------------------------------------------------------------------------|---------|-----------------------------|
| connection | The name of a connection declared in the top-level `x-tele` extension         | string  | empty                       |
| name       | Name of the intercept engagement                                              | string  | name of the compose service |
| workload   | Name of the remote workload (typically the deployment)                        | string  | name of the engagement      |
| ports      | Service &lt;local port&gt;:&lt;service port&gt; to intercept                  | strings | empty                       |
| service    | Name of the remote service                                                    | string  | empty                       |
| to-pod     | Ports to forward from the local compose service to the remote pod's localhost | strings | empty                       |


The `service` field is optional as long as the given `ports` are unique.
The `workload` is useful when doing multiple intercepts on the same workload using different intercept names.


### replace

The `replace` ensures that the docker compose service receives traffic from, and shares the environment and volumes of, a remote container in the cluster. The extension can have the following fields set:

| Name       | Description                                                                   | Type    | Default Value               |
|------------|-------------------------------------------------------------------------------|---------|-----------------------------|
| connection | The name of a connection declared in the top-level `x-tele` extension         | string  | empty                       |
| name       | Name of the remote workload (typically the deployment)                        | string  | name of the compose service |
| container  | Name of the remote container                                                  | string  | empty                       |
| ports      | Service &lt;local port&gt;:&lt;container port&gt; to replace                  | strings | empty                       |
| to-pod     | Ports to forward from the local compose service to the remote pod's localhost | strings | empty                       |

### wiretap

The `wiretap` ensures that the docker compose service receives wiretapped traffic from a remote service, and shares the environment and volumes (read-only) of, a remote container. The extension can have the following fields set:

| Name       | Description                                                                   | Type    | Default Value               |
|------------|-------------------------------------------------------------------------------|---------|-----------------------------|
| connection | The name of a connection declared in the top-level `x-tele` extension         | string  | empty                       |
| name       | Name of the wiretapped workload  (typically the deployment)                   | string  | name of the compose service |
| ports      | Service &lt;local port&gt;:&lt;service port&gt; to wiretap                    | strings | empty                       |
| service    | Name of the remote service                                                    | string  | empty                       |
| to-pod     | Ports to forward from the local compose service to the remote pod's localhost | strings | empty                       |

The `service` field is optional as long as the given `service ports` are unique.
