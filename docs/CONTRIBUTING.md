# Telepresence Documentation

This folder contains the Telepresence documentation in a format suitable for a versioned folder in the
telepresenceio/telepresence.io repository. The folder will show up in that repository when a new minor revision
tag is created here.

Assuming that a 2.20.0 release is pending, and that a release/v2.20.0 branch has been created, then:
```console
$ export TELEPRESENCE_VERSION=v2.20.0
$ make prepare-release
$ git push origin {,rpc/}v2.20.0 release/v2.20.0
```

will result in a `docs/v2.20` folder with this folder's contents in the telepresenceio/telepresence.io repository.

Subsequent bugfix tags for the same minor tag, i.e.:
```console
$ export TELEPRESENCE_VERSION=v2.20.1
$ make prepare-release
$ git push origin {,rpc/}v2.20.1 release/v2.20.1
```
will not result in a new folder when it is pushed, but it will update the content of the `docs/v2.20` folder to
reflect this folder's content for that tag.
