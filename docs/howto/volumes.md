# Volume access

Volume support requires a small amount of work on your part.
The root directory where all the volumes can be found will be set to the `TELEPRESENCE_ROOT` environment variable in the shell run by `telepresence`.
You will then need to use that env variable as the root for volume paths you are opening.

For example, all Kubernetes containers have a volume mounted at `/var/run/secrets` with the service account details.
Those files are accessible from Telepresence:

```console
$ telepresence --run-shell
Starting proxy...
@minikube|$ echo $TELEPRESENCE_ROOT
/tmp/tmpk_svwt_5
@minikube|$ ls $TELEPRESENCE_ROOT/var/run/secrets/kubernetes.io/serviceaccount/
ca.crt  namespace  token
```

The files are available at a different path than they are on the actual Kubernetes environment.

One way to deal with that is to change your application's code slightly.
For example, let's say you have a volume that mounts a file called `/app/secrets`.
Normally you would just open it in your code like so:


```python
secret_file = open("/app/secrets")
```

To support volume proxying by Telepresence, you will need to change your code, for example:

```python
volume_root = "/"
if "TELEPRESENCE_ROOT" in os.environ:
    volume_root = os.environ["TELEPRESENCE_ROOT"]
secret_file = open(os.path.join(volume_root, "app/secrets"))
```

By falling back to `/` when the environment variable is not set your code will continue to work in its normal Kubernetes setting.

Another way you can do this is by using the [proot](http://proot-me.github.io/) utility on Linux, which allows you to do fake bind mounts without being root.
For example, presuming you've installed `proot` (`apt install proot` on Ubuntu), in the following example we bind `$TELEPRESENCE_ROOT/var/run/secrets` to `/var/run/secrets`.
That means code doesn't need to be modified as the paths are in the expected location:

```console
@minikube|$ proot -b $TELEPRESENCE_ROOT/var/run/secrets/:/var/run/secrets bash
$ ls /var/run/secrets/kubernetes.io/serviceaccount/
ca.crt  namespace  token
```
