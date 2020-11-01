# Developing Telepresence 2

## Build the binaries

```console
$ make build
[...]
```

## Deploy the Manager to your cluster

```console
$ export KO_DOCKER_REPO=docker.io/ark3
$ ko apply -f k8s
2020/10/27 10:25:37 Using base docker.io/datawire/telepresence-k8s:0.101 for github.com/datawire/telepresence2/cmd/traffic
2020/10/27 10:25:37 Building github.com/datawire/telepresence2/cmd/traffic for linux/amd64
2020/10/27 10:25:38 Publishing docker.io/ark3/traffic-6c3ca0a9c236a15e275ec10cceb31334:latest
2020/10/27 10:25:39 mounted blob: sha256:5a3ea8efae5d0abb93d2a04be0a4870087042b8ecab8001f613cdc2a9440616a
[...]
2020/10/27 10:25:42 pushed blob: sha256:7800000435c9f977dc30d9403491b92358a5b60530d87f224c06864ec8eda4ca
2020/10/27 10:25:42 docker.io/ark3/traffic-6c3ca0a9c236a15e275ec10cceb31334:latest: digest: sha256:9e7a0da8df486319a93e4f8f1ba75d7d1a793e1194efbbc37a36ed4e7416aac8 size: 2042
2020/10/27 10:25:42 Published docker.io/ark3/traffic-6c3ca0a9c236a15e275ec10cceb31334@sha256:9e7a0da8df486319a93e4f8f1ba75d7d1a793e1194efbbc37a36ed4e7416aac8
service/echo-easy configured
deployment.apps/echo-easy configured
service/traffic-manager configured
deployment.apps/traffic-manager configured
```

Now you can run Telepresence against this cluster and try out `curl echo-easy` to see outbound at work.

## Build an image

```console
$ make image
2020/11/01 16:41:29 Using base docker.io/datawire/telepresence-k8s:0.101 for github.com/datawire/telepresence2/cmd/traffic
2020/11/01 16:41:30 Building github.com/datawire/telepresence2/cmd/traffic for linux/amd64
2020/11/01 16:41:31 Loading ko.local/traffic-6c3ca0a9c236a15e275ec10cceb31334:de195d725fc4f2018208fa5f9a2154af805e3fa6b17be2276e8bba3638f0dfe9
2020/11/01 16:41:34 Loaded ko.local/traffic-6c3ca0a9c236a15e275ec10cceb31334:de195d725fc4f2018208fa5f9a2154af805e3fa6b17be2276e8bba3638f0dfe9
2020/11/01 16:41:34 Adding tag latest
2020/11/01 16:41:34 Added tag latest
docker tag ko.local/traffic-6c3ca0a9c236a15e275ec10cceb31334:de195d725fc4f2018208fa5f9a2154af805e3fa6b17be2276e8bba3638f0dfe9 docker.io/datawire/tel2:v0.1.0-7-g2c1ce37-1604266889
```

The image is now in your machine's Docker daemon and tagged as `docker.io/datawire/tel2:v0.1.0-7-g2c1ce37-1604266889` in this example. You can `docker push` it manually if you'd like. You can override `TELEPRESENCE_REGISTRY` and `VERSION_SUFFIX` as needed; see `make help` for the defaults.

