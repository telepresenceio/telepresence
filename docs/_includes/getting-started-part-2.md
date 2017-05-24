Once you know the address you can send it a query and it will get routed to your locally running server:

```console
$ curl http://104.197.103.13:8080/file.txt
hello from your laptop
```

Finally, let's kill Telepresence locally so you don't have to worry about other people accessing your local web server:

```console
$ fg
telepresence --deployment myserver --expose 8080 --run python3 -m http.server 8080
^C
Keyboard interrupt received, exiting.
```

Telepresence can do much more than this, which we'll cover in the reference section of the documentation, on your left.
