.PHONY: default build build-local build-remote

VERSION=0.0

default:
	echo "Run 'make build' to build Docker image."

build: build-local build-remote

build-local:
	cd local && docker build . -t datawire/telepresence-local

build-remote:
	cd remote && docker build . -t datawire/telepresence-k8s:dev

virtualenv:
	virtualenv virtualenv

release: virtualenv
	virtualenv/bin/pip install bumpversion
	virtualenv/bin/bumpversion --verbose --list minor
