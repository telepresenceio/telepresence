import Alert from '@material-ui/lab/Alert';

# Cluster-side configuration

For the most part, Telepresence doesn't require any special
configuration in the cluster and can be used right away in any
cluster (as long as the user has adequate [RBAC permissions](../rbac)).

However, some advanced features do require some configuration in the
cluster.

## TLS

In this example, other applications in the cluster expect to speak TLS to your
intercepted application (perhaps you're using a service-mesh that does
mTLS).

In order to use `--mechanism=http` (or any features that imply
`--mechanism=http`) you need to tell Telepresence about the TLS
certificates in use.

Tell Telepresence about the certificates in use by adjusting your
[workload's](../intercepts/#supported-workloads) Pod template to set a couple of
annotations on the intercepted Pods:

```diff
 spec:
   template:
     metadata:
       labels:
         service: your-service
+      annotations:
+        "getambassador.io/inject-terminating-tls-secret": "your-terminating-secret"  # optional
+        "getambassador.io/inject-originating-tls-secret": "your-originating-secret"  # optional
     spec:
+      serviceAccountName: "your-account-that-has-rbac-to-read-those-secrets"
       containers:
```

- The `getambassador.io/inject-terminating-tls-secret` annotation
  (optional) names the Kubernetes Secret that contains the TLS server
  certificate to use for decrypting and responding to incoming
  requests.

  When Telepresence modifies the Service and workload port
  definitions to point at the Telepresence Agent sidecar's port
  instead of your application's actual port, the sidecar will use this
  certificate to terminate TLS.

- The `getambassador.io/inject-originating-tls-secret` annotation
  (optional) names the Kubernetes Secret that contains the TLS
  client certificate to use for communicating with your application.

  You will need to set this if your application expects incoming
  requests to speak TLS (for example, your
  code expects to handle mTLS itself instead of letting a service-mesh
  sidecar handle mTLS for it, or the port definition that Telepresence
  modified pointed at the service-mesh sidecar instead of at your
  application).

  If you do set this, you should to set it to the
  same client certificate Secret that you configure the Ambassador
  Edge Stack to use for mTLS.

It is only possible to refer to a Secret that is in the same Namespace
as the Pod.

The Pod will need to have permission to `get` and `watch` each of
those Secrets.

Telepresence understands `type: kubernetes.io/tls` Secrets and
`type: istio.io/key-and-cert` Secrets; as well as `type: Opaque`
Secrets that it detects to be formatted as one of those types.

## Air gapped cluster

If your cluster is on an isolated network such that it cannot
communicate with Ambassador Cloud, then some additional configuration
is required to acquire a license key in order to use personal
intercepts.

### Create a license

1. Go to [the teams setting page in Ambassador Cloud](https://auth.datawire.io/redirects/settings/teams) and
select *Licenses* for the team you want to create the license for.

2. Generate a new license (if one doesn't already exist) by clicking *Generate New License*.

3. You will be prompted for your Cluster ID. Ensure your
kubeconfig context is using the cluster you want to create a license for then
run this command to generate the Cluster ID:

  ```
  $ telepresence current-cluster-id

    Cluster ID: <some UID>
  ```

4. Click *Generate API Key* to finish generating the license.

### Add license to cluster

1. On the licenses page, download the license file associated with your cluster.

2. Use this command to generate a Kubernetes Secret config using the license file:

  ```
  $ telepresence license -f <downloaded-license-file>

    apiVersion: v1
    data:
      hostDomain: <long_string>
      license: <longer_string>
    kind: Secret
    metadata:
      creationTimestamp: null
      name: systema-license
      namespace: ambassador
  ```

3. Save the output as a YAML file and apply it to your
cluster with `kubectl`.

4. Ensure that you have the docker image for the Smart Agent (datawire/ambassador-telepresence-agent:1.8.0)
pulled and in a registry your cluster can pull from.

5. Have users use the `images` [config key](../config/#images) keys so telepresence uses the aforementioned image for their agent.

Users will now be able to use preview intercepts with the
`--preview-url=false` flag.  Even with the license key, preview URLs
cannot be used without enabling direct communication with Ambassador
Cloud, as Ambassador Cloud is essential to their operation.

If using Helm to install the server-side components, see the chart's [README](https://github.com/telepresenceio/telepresence/tree/release/v2/charts/telepresence) to learn how to configure the image registry and license secret.

Have clients use the [skipLogin](../config/#cloud) key to ensure the cli knows it is operating in an
air-gapped environment.

## Mutating Webhook

By default, Telepresence updates the intercepted workload (Deployment, StatefulSet, ReplicaSet)
template to add the [Traffic Agent](../architecture/#traffic-agent) sidecar container and update the
port definitions. If you use GitOps workflows (with tools like ArgoCD) to automatically update your
cluster so that it reflects the desired state from an external Git repository, this behavior can make
your workload out of sync with that external desired state.

To solve this issue, you can use Telepresence's Mutating Webhook alternative mechanism. Intercepted
workloads will then stay untouched and only the underlying pods will be modified to inject the Traffic
Agent sidecar container and update the port definitions.

<Alert severity="info">
A current limitation of the Mutating Webhook mechanism is that the <code>targetPort</code> of your intercepted
Service needs to point to the <strong>name</strong> of a port on your container, not the port number itself.
</Alert>

Simply add the `telepresence.getambassador.io/inject-traffic-agent: enabled` annotation to your
workload template's annotations:

```diff
 spec:
   template:
     metadata:
       labels:
         service: your-service
+      annotations:
+        telepresence.getambassador.io/inject-traffic-agent: enabled
     spec:
       containers:
```

### Service Port Annotation

A service port annotation can be added to the workload to make the Mutating Webhook select a specific port
in the service. This is necessary when the service has multiple ports.

```diff
 spec:
   template:
     metadata:
       labels:
         service: your-service
       annotations:
         telepresence.getambassador.io/inject-traffic-agent: enabled
+        telepresence.getambassador.io/inject-service-port: https
     spec:
       containers:
```
