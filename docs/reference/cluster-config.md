import Alert from '@material-ui/lab/Alert';

# Cluster-side configuration

For the most part, Telepresence doesn't require any special
configuration in the cluster and can be used right away in any
cluster (as long as the user has adequate [RBAC permissions](rbac.md)
and the cluster's server version is `1.19.0` or higher).

## Helm Chart configuration
Some cluster specific configuration can be provided when installing
or upgrading the Telepresence cluster installation using Helm. Once
installed, the Telepresence client will configure itself from values
that it receives when connecting to the Traffic manager.

See the Helm chart [README](https://artifacthub.io/packages/helm/telepresence-oss/telepresence-oss/$version$)
for a full list of available configuration settings.

### Values
To add configuration, create a yaml file with the configuration values and then use it executing `telepresence helm install [--upgrade] --values <values yaml>`

## Client Configuration

It is possible for the Traffic Manager to automatically push config to all
connecting clients. To learn more about this, please see the [client config docs](config.md#global-configuration)

## Traffic Manager Configuration

The `trafficManager` structure of the Helm chart configures the behavior of the Telepresence traffic manager.

## Agent Configuration

The `agent` structure of the Helm chart configures the behavior of the Telepresence agents.

### Image Configuration

The `agent.image` structure contains the following values:

| Setting    | Meaning                                                                     |
|------------|-----------------------------------------------------------------------------|
| `registry` | Registry used when downloading the image. Defaults to "docker.io/datawire". |
| `name`     | The name of the image. Defaults to "tel2"                                   |
| `tag`      | The tag of the image. Defaults to $version$                                 |

### Log level

The `agent.LogLevel` controls the log level of the traffic-agent. See [Log Levels](config.md#log-levels) for more info.

### Resources

The `agent.resources` and `agent.initResources` will be used as the `resources` element when injecting traffic-agents and init-containers.

## Mutating Webhook

Telepresence uses a Mutating Webhook to inject the [Traffic Agent](architecture.md#traffic-agent) sidecar container and update the
port definitions. This means that an intercepted workload (Deployment, StatefulSet, ReplicaSet) will remain untouched
and in sync as far as GitOps workflows (such as ArgoCD) are concerned.

The injection will happen on demand the first time an attempt is made to intercept the workload.

If you want to prevent that the injection ever happens, simply add the `telepresence.getambassador.io/inject-traffic-agent: disabled`
annotation to your workload template's annotations:

```diff
 spec:
   template:
     metadata:
       labels:
         service: your-service
+      annotations:
+        telepresence.getambassador.io/inject-traffic-agent: disabled
     spec:
       containers:
```

### Service Name and Port Annotations

Telepresence will automatically find all services and all ports that will connect to a workload and make them available
for an intercept, but you can explicitly define that only one service and/or port can be intercepted.

```diff
 spec:
   template:
     metadata:
       labels:
         service: your-service
       annotations:
+        telepresence.getambassador.io/inject-service-name: my-service
+        telepresence.getambassador.io/inject-service-port: https
     spec:
       containers:
```

### Ignore Certain Volume Mounts

An annotation `telepresence.getambassador.io/inject-ignore-volume-mounts` can be used to make the injector ignore certain volume mounts denoted by a comma-separated string. The specified volume mounts from the original container will not be appended to the agent sidecar container.

```diff
 spec:
   template:
     metadata:
       annotations:
+        telepresence.getambassador.io/inject-ignore-volume-mounts: "foo,bar"
     spec:
       containers:
```

### Note on Numeric Ports

If the <code>targetPort</code> of your intercepted service is pointing at a port number, in addition to
injecting the Traffic Agent sidecar, Telepresence will also inject an <code>initContainer</code> that will
reconfigure the pod's firewall rules to redirect traffic to the Traffic Agent.

<Alert severity="info">
Note that this <code>initContainer</code> requires `NET_ADMIN` capabilities.
If your cluster administrator has disabled them, you will be unable to use numeric ports with the agent injector.
</Alert>

For example, the following service is using a numeric port, so Telepresence would inject an initContainer into it:
```yaml
apiVersion: v1
kind: Service
metadata:
  name: your-service
spec:
  type: ClusterIP
  selector:
    service: your-service
  ports:
    - port: 80
      targetPort: 8080
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: your-service
  labels:
    service: your-service
spec:
  replicas: 1
  selector:
    matchLabels:
      service: your-service
  template:
    metadata:
      annotations:
        telepresence.getambassador.io/inject-traffic-agent: enabled
      labels:
        service: your-service
    spec:
      containers:
        - name: your-container
          image: jmalloc/echo-server
          ports:
            - containerPort: 8080
```

## Excluding Envrionment Variables

If your pod contains sensitive variables like a database password, or third party API Key, you may want to exclude those from being propagated through an intercept.
Telepresence allows you to configure this through a ConfigMap that is then read and removes the sensitive variables. 

This can be done in two ways: 

When installing your traffic-manager through helm you can use the `--set` flag and pass a comma separated list of variables:

`telepresence helm install --set intercept.environment.excluded="{DATABASE_PASSWORD,API_KEY}"`

This also applies when upgrading:

`telepresence helm upgrade --set intercept.environment.excluded="{DATABASE_PASSWORD,API_KEY}"`

Once this is completed, the environment variables will no longer be in the environment file created by an Intercept.

The other way to complete this is in your custom `values.yaml`. Customizing your traffic-manager through a values file can be viewed [here](../install/manager.md).

```yaml
intercept:
  environment:
    excluded: ['DATABASE_PASSWORD', 'API_KEY']
```

You can exclude any number of variables, they just need to match the `key` of the variable within a pod to be excluded.
