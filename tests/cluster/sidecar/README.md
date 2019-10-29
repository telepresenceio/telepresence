# Sidecar test helper

This image serves on `http://localhost:8910/` and tries to reach `http://localhost:9876/` when responding to a request. If the sub-request succeeds, then the original request succeeds as well, with the same body content. The goal is to run as a non-swapped container in a swap deployment scenario to help test Telepresence's Pod networking features.

```shell
docker build . -t datawire/tel-sidecar-test-helper:1
docker push datawire/tel-sidecar-test-helper:1
```
