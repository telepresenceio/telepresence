# Telepresence

[Telepresence](https://www.getambassador.io/products/telepresence/) is a tool 
that allows for local development of microservices running in a remote 
Kubernetes cluster.

This chart manages the server-side components of Telepresence so that an
operations team can give limited access to the cluster for developers to work on
their services.

## Install

```sh
helm repo add datawire https://getambassador.io
helm install traffic-manager -n ambassador datawire/telepresence --create-namespace
```

## Changelog

Notable chart changes are listed in the [CHANGELOG](./CHANGELOG.md)

## Configuration

The following tables lists the configurable parameters of the Ambassador chart and their default values.

| Parameter                | Description                                                                                                             | Default                                                                                           |
|--------------------------|-------------------------------------------------------------------------------------------------------------------------|---------------------------------------------------------------------------------------------------|
| image.repository         | The repository to download the image from. Set `TELEPRESENCE_REGISTRY=image.repository` locally if changing this value. | `docker.io/datawire/tel2`                                                                         |
| image.pullPolicy         | How the `Pod` will attempt to pull the image.                                                                           | `IfNotPresent`                                                                                    |
| image.tag                | Override the version of the Traffic Manager to be installed.                                                            | `""` (Defined in `appVersion` Chart.yaml)                                                         |
| image.imagePullSecrets   | The `Secret` storing any credentials needed to access the image in a private registry.                                  | `[]`                                                                                              |
| podAnnotations           | Annotations for the Traffic Manager `Pod`                                                                               | `{}`                                                                                              |
| podSecurityContext       | The Kubernetes SecurityContext for the `Pod`                                                                            | `{}`                                                                                              |
| securityContext          | The Kubernetes SecurityContext for the `Deployment`                                                                     | `{"readOnlyRootFilesystem": true, "runAsNonRoot": true, "runAsUser": 1000}`                       |
| service.type             | The type of `Service` for the Traffic Manager.                                                                          | `ClusterIP`                                                                                       |
| service.ports            | The ports the Traffic Manager `Service` will listen on and forward to. **Do not change.**                               | `[{"name":"sshd","port":8022,"targetPort":"sshd"},{"name":"api","port":8081,"targetPort":"api"}]` |
| resources                | Define resource requests and limits for the Traffic Manger.                                                             | `{}`                                                                                              |
| nodeSelector             | Define which `Node`s you want to the Traffic Manager to be deployed to.                                                 | `{}`                                                                                              |
| tolerations              | Define tolerations for the Traffic Manager to ignore `Node` taints.                                                     | `[]`                                                                                              |
| affinity                 | Define the `Node` Affinity and Anti-Affinity for the Traffic Manager.                                                   | `{}`                                                                                              |
| licenseKey.create        | Create the license key `volume` and `volumeMount`. **Only required for clusters without access to the internet.**       | `false`                                                                                           |
| licenseKey.value         | The value of the license key.                                                                                           | `""`                                                                                              |
| licenseKey.secret.create | Define whether you want the license key `Secret` to be managed by the release or not.                                   | `true`                                                                                            |
| licenseKey.secret.name   | The name of the `Secret` that Traffic Manager will look for.                                                            | `systema-license`                                                                                 |

## License Key 

Telepresence can create TCP intercepts without a license key. Creating 
intercepts based on HTTP headers requires a license key from the Ambassador
Cloud.

In normal environments that have access to the public internet, the Traffic
Manager will automatically connect to the Ambassador Cloud to retrieve a license
key. If you are working in one of these environments, you can safely ignore
these settings in the chart.

If you are running in an [air gapped cluster](https://www.getambassador.io/docs/telepresence/latest/reference/cluster-config/#air-gapped-cluster),
you will need to configure the Traffic Manager to use a license key you manually
deploy to the cluster.

These notes should help clarify your options for enabling this.

* `licenseKey.create` will **always** create the `volume` and `volumeMount` for
mounting the `Secret` in the Traffic Managed

* `licenseKey.secret.name` will define the name of the `Secret` that is
mounted in the Traffic Manager, regardless of it it is created by the chart

* `licenseKey.secret.create` will create a `Secret` with
   ```
   data:
     license: {{.licenseKey.value}}
   ```