# Intercepts 

## Intercept Behavior When Logged into Ambassador Cloud

After logging into Ambassador Cloud (with `telepresence login`), Telepresence will default to `--preview-url=true`, which will use Ambassador Cloud to create a sharable preview URL for this intercept. (Creating an intercept without logging in will default to `--preview-url=false`).

In order to do this, it will prompt you for four options.  For the first, `Ingress`, Telepresence tries to intelligently determine the ingress controller deployment and namespace for you.  If they are correct, you can hit `enter` to accept the defaults.  Set the next two options, `TLS` and `Port`, appropriately based on your ingress service. The fourth is a hostname for the service, if required by your ingress.

Also because you're logged in, Telepresence will default to `--mechanism=http --http-match=auto` (or just `--http-match=auto`; `--http-match` implies `--mechanism=http`). If you hadn't been logged in it would have defaulted to `--mechanism=tcp`.  This tells it to do smart intercepts and only intercept a subset of HTTP requests, rather than just intercepting the entirety of all TCP connections.  This is important for working in a shared cluster with teammates, and is important for the preview URL functionality.  See `telepresence intercept --help` for information on using `--http-match` to customize which requests it intercepts.

## Specifying a namespace for an intercept

The namespace of the intercepted deployment is specified using the `--namespace` option. When this option is used, and `--deployment` is not used, then the given name is interpreted as the name of the deployment and the name of the intercept will be constructed from that name and the namespace.

```
telepresence intercept hello --namespace myns --port 9000
```

This will intercept a Deployment named "hello" and name the intercept
"hello-myns".  In order to remove the intercept, you will need to run
`telepresence leave hello-mydns` instead of just `telepresence leave
hello`.

The name of the intercept will be left unchanged if the deployment is specified.

```
telepresence intercept myhello --namespace myns --deployment hello --port 9000
```

This will intercept a deployment named "hello" and name the intercept "myhello".

## Importing Environment Variables

Telepresence can import the environment variables from the pod that is being intercepted, see [this doc](../environment/) for more details.

## Creating an Intercept Without a Preview URL

If you *are not* logged into Ambassador Cloud, the following command will intercept all traffic bound to the service and proxy it to your laptop. This includes traffic coming through your ingress controller, so use this option carefully as to not disrupt production environments.

```
telepresence intercept <deployment name> --port=<TCP port>
```

If you *are* logged into Ambassador Cloud, setting the `preview-url` flag to `false` is necessary.

```
telepresence intercept <deployment name>  --port=<TCP port> --preview-url=false
```

This will output a header that you can set on your request for that traffic to be intercepted:

```
$ telepresence intercept <deployment name>  --port=<TCP port> --preview-url=false
  
  Using deployment <deployment name> 
  intercepted
      Intercept name: <full name of intercept>
      State         : ACTIVE
      Destination   : 127.0.0.1:<local TCP port>
      Intercepting  : HTTP requests that match all of:
        header("x-telepresence-intercept-id") ~= regexp("<uuid unique to you>:<full name of intercept>")
```

Run `telepresence status` to see the list of active intercepts.

```
$ telepresence status
  
  Connected
    Context:       default (https://<cluster public IP>)
    Proxy:         ON (networking to the cluster is enabled)
    Intercepts:    1 total
      <deployment name>: <your laptop name>
```

Finally, run `telepresence leave <name of intercept>` to stop the intercept.
