# Datawire build-aux

This is a collection of Makefile snippets (and associated utilities)
for use in Datawire projects.

## How to use

Add `build-aux.git` as `build-aux/` in the git repository of the
project that you want to use this from.  I recommend that you do this
using `git subtree`, but `git submodule` is fine too.

Then, in your Makefile, write `include build-aux/FOO.mk` for each
common bit of functionality that you want to make use of.

### Using `git-subtree` to manage `./build-aux/`

 - Start using build-aux:

       $ git subtree add --squash --prefix=build-aux git@github.com:datawire/build-aux.git master

 - Update to latest build-aux:

       $ ./build-aux/build-aux-pull

 - Push "vendored" changes upstream to build-aux.git:

       $ ./build-aux/build-aux-push

## Go

Currently, there are 2 options for Go projects:

 - `go-workspace.mk`: For GOPATH workspaces
 - `go-mod.mk`: For Go 1.11 modules

### Initializing a `go-workspace.mk` project

	$ MODULE=EXAMPLE.COM/YOU/GITREPO

	$ echo 'include build-aux/go-workspace.mk' >> Makefile
	$ echo /.go-workpsace >> .gitignore
	$ echo "!/.go-workspace/src/${MODULE}" >> .gitignore
	$ mkdir -p $(dirname .go-workspace/src/${MODULE})
	$ ln -s $(dirname .go-workspace/src/${MODULE} | sed -E 's,[^/]+,..,g') .go-workspace/src/${MODULE}

What's that big expression in the `ln -s` command!?  It's the same as

	$ ln -sr . .go-workspace/src/${MODULE}

but for lame operating systems that ship an `ln` that doesn't
understand the `-r` flag.

### Initializing a `go-mod.mk` project

	$ go mod init EXAMPLE.COM/YOU/GITREPO

	$ echo 'include build-aux/go-mod.mk' >> Makefile

### Migrating from `go-workspace.mk` to `go-mod.mk`

	$ go mod init EXAMPLE.COM/YOU/GITREPO

	$ make clobber
	$ rm -rf -- .go-workspace vendor glide.* Gopkg.*
	$ sed -i -E 's,/go-workspace\.mk,/go-mod.mk,' Makefile
	$ sed -i -e '/\.go-workspace/d' .gitignore
