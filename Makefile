.PHONY: default build build-local build-remote bumpversion release local-test

VERSION=$(shell git describe --tags)

default:
	@echo "To release:"
	@echo "1. 'make bumpversion'"
	@echo "2. do git push as instructed by bumpversion"
	@echo "3. 'make release'"

version:
	@echo $(VERSION)

build: build-local build-remote

build-local:
	cd local && docker build . -t datawire/telepresence-local:$(VERSION)

build-remote:
	cd remote && docker build . -t datawire/telepresence-k8s:$(VERSION)

virtualenv:
	virtualenv --python=python3 virtualenv
	virtualenv/bin/pip install -r dev-requirements.txt
	virtualenv/bin/pip install -r remote/requirements.txt

bumpversion: virtualenv
	virtualenv/bin/bumpversion --verbose --list minor
	@echo "Please run: git push origin master --tags"

local-test: virtualenv
	@echo "IMPORTANT: this will change kubectl context to minikube!\n\n"
	cd local && sudo docker build . -q -t datawire/telepresence-local:$(VERSION)
	eval $(shell minikube docker-env) && \
		cd remote && \
		docker build . -q -t datawire/telepresence-k8s:$(VERSION)
	kubectl config set-context minikube
	env TELEPRESENCE_VERSION=$(VERSION) ci/test.sh

release: build
	docker push datawire/telepresence-local:$(VERSION)
	docker push datawire/telepresence-k8s:$(VERSION)
