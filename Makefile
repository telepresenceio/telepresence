.PHONY: default build build-local build-remote

default:
	echo "Run 'make build' to build Docker image."

build: build-local build-remote

build-local:
	cd local && docker build . -t datawire/local-telepresence

build-remote:
	cd remote && docker build . -t datawire/telepresence-remote:dev
