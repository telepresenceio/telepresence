.PHONY: default build-remote bumpversion release minikube-test build-remote-minikube

VERSION=$(shell git describe --tags)

default:
	@echo "To release:"
	@echo "1. 'make bumpversion'"
	@echo "2. do git push as instructed by bumpversion"
	@echo "3. 'make release'"

version:
	@echo $(VERSION)

build-remote:
	cd remote && sudo docker build . -t datawire/telepresence-k8s:$(VERSION)

virtualenv:
	virtualenv --python=python3 virtualenv
	virtualenv/bin/pip install -r dev-requirements.txt
	virtualenv/bin/pip install -r remote/requirements.txt

bumpversion: virtualenv
	virtualenv/bin/bumpversion --verbose --list minor
	@echo "Please run: git push origin master --tags"

build-remote-minikube:
	eval $(shell minikube docker-env) && \
		cd remote && \
		docker build . -q -t datawire/telepresence-k8s:$(VERSION)

# Run tests in minikube:
minikube-test: virtualenv
	@echo "IMPORTANT: this will change kubectl context to minikube!\n\n"
	kubectl config use-context minikube
	env TELEPRESENCE_VERSION=$(VERSION) ci/test.sh

release: build-remote
	sudo docker push datawire/telepresence-k8s:$(VERSION)
	env TELEPRESENCE_VERSION=$(VERSION) packaging/homebrew-package.sh
	packaging/create-linux-packages.py $(VERSION)
	packaging/upload-linux-packages.py $(VERSION)
