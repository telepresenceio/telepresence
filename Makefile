SHELL := /usr/bin/env bash

GIT_COMMIT=$(shell git rev-parse --verify HEAD)

GOOS = $(shell go env GOOS)
GOARCH = $(shell go env GOARCH)
GOBUILD = go build -o bin/$(BINARY_BASENAME)-$(GOOS)-$(GOARCH)

BINARY_BASENAME=watt

all: clean build

build:
	$(GOBUILD) cmd/$(BINARY_BASENAME)/main.go
	ln -sf $(BINARY_BASENAME)-$(GOOS)-$(GOARCH) bin/$(BINARY_BASENAME)

build.image:
	docker build \
	-t datawireio/$(BINARY_BASENAME) \
	-t datawireio/$(BINARY_BASENAME):$(GIT_COMMIT) \
	-f Dockerfile \
	.

#build.image.devtools:
#	docker build \
#	--build-arg UID=$(shell id -u) \
#	-t knaut-dev \
#	-f hack/docker/dev/Dockerfile \
#	hack/docker/dev

clean:
	rm -rf bin

#cloc: build.image.devtools
#	docker run \
#	--rm -it \
#	--volume $(PWD):/project:ro \
#	--workdir /project \
#	$(BINARY_BASENAME)-dev \
#	/usr/bin/cloc .

test.fast:
	go test -tags=gorgonzola -v ./...

consul.local:
	docker run --rm -it -p8500:8500 --name=consul consul:1.4.3

consul.attach:
	docker exec -it consul /bin/sh
