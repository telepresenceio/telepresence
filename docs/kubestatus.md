The kubestatus program provides a command line interface for updating
the status of a kubernetes resource.

The kubestatus program operates on a set of resources. This set is
defined by the combination of the <kind> passed on the command line,
and the --field-selector and --label-selector arguments, e.g.:

```
kubestatus service -f metadata.name=foo
```

Note that by default kubestatus will just list the statuses you have
selected. If you want to update the status, you need to supply it with
a new status via the -u or --update flag:

```
kubestatus service -f metadata.name=foo -u updated-status.json
```

The status must be supplied in a valid json format in the referenced
file. This status also needs to conform to whatever the kubernetes
schema is for the given resource. You can use kubectl explain to
figure out what that is, e.g.:

```
kubectl explain service.status
...
kubectl explain service.status.loadBalancer
...
```
