.PHONY: default build build-local build-remote bumpversion release test

VERSION=0.8

default:
	@echo "To release:"
	@echo "1. 'make bumpversion'"
	@echo "2. do git push as instructed by bumpversion"
	@echo "3. 'make release'"

build: build-local build-remote

build-local:
	cd local && docker build . -t datawire/telepresence-local:$(VERSION)

build-remote:
	cd remote && docker build . -t datawire/telepresence-k8s:$(VERSION)

virtualenv:
	virtualenv --python=python3 virtualenv
	virtualenv/bin/pip install -r dev-requirements.txt

bumpversion: virtualenv
	virtualenv/bin/bumpversion --verbose --list minor
	@echo "Please run: git push origin master --tags"

test: virtualenv
	@echo "IMPORTANT: this will change kubectl context to minikube!\n\n"
	cd local && sudo docker build . -q -t datawire/telepresence-local:$(VERSION)
	eval $(shell minikube docker-env) && \
		cd remote && \
		docker build . -q -t datawire/telepresence-k8s:$(VERSION)
	kubectl config set-context minikube
	env PATH=$(PWD)/cli/:$(PATH) virtualenv/bin/py.test tests

release: build
	docker push datawire/telepresence-local:$(VERSION)
	docker push datawire/telepresence-k8s:$(VERSION)
