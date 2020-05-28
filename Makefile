# Copyright 2018 Datawire. All rights reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# If switching from docker.io back to gcr.io, then you must also
# uncomment .circleci/config.yml's 'build-image' job's gcloud
# authentication.
TELEPRESENCE_REGISTRY ?= docker.io/datawire
VERSION_SUFFIX        ?= -$(OS)-$(TIME)
DOCKER_PUSH           ?= docker-push
PYTEST_ARGS           ?=

#

_OS := $(shell python3 -c 'from sys import platform; print({"linux": "LNX", "darwin": "OSX"}.get(platform))')
_TIME := $(shell date +%s)
TELEPRESENCE_VERSION := $(shell git describe --tags)$(foreach OS,$(_OS),$(foreach TIME,$(_TIME),$(VERSION_SUFFIX)))

default: help
	@echo
	@echo "See                 ./docs/reference/developing.md"
	@echo "or https://telepresence.io/reference/developing.html"
.PHONY: default

# Attempt to get credentials cached early on while the user is still looking at
# the terminal. They'll be required later on during the test suite run and the
# prompt is likely to be buried in test output at that point.
acquire-sudo:
	sudo echo -n
.PHONY: acquire-sudo

#

check-local: virtualenv  ## Run the local tests (fast, doesn't require a Kubernetes cluster)
	$(VIRTUALENV) py.test -v --timeout=360 --timeout-method=thread -rfE tests/local k8s-proxy $(PYTEST_ARGS)
.PHONY: check-local

check-cluster: acquire-sudo virtualenv $(DOCKER_PUSH)  ## Run the end-to-end tests (requires a cluster, implies '$(DOCKER_PUSH)')
	$(if $(shell which kubectl 2>/dev/null),,$(error Required executable 'kubectl' not found on $$PATH))
	sudo echo -n
	$(VIRTUALENV) $(_pytest_env) py.test -v --timeout=360 --timeout-method=thread -rfE tests/cluster $(PYTEST_ARGS)
.PHONY: check-cluster

_pytest_env  = TELEPRESENCE_REGISTRY=$(TELEPRESENCE_REGISTRY)
_pytest_env += TELEPRESENCE_VERSION=$(TELEPRESENCE_VERSION)
_pytest_env += SCOUT_DISABLE=1
check: acquire-sudo check-local check-cluster  ## Run the full test suite (local and cluster)
.PHONY: check

_testbench_vars  = TELEPRESENCE_REGISTRY=$(TELEPRESENCE_REGISTRY)
_testbench_vars += TELEPRESENCE_VERSION=$(TELEPRESENCE_VERSION)
_testbench_vars += DOCKER_PUSH=''
_testbench_vars += PYTEST_ARGS='$(call escape_squotes,--tap-combined $(PYTEST_ARGS))'
testbench-check: $(DOCKER_PUSH)  ## Run the test suite in testbench (implies '$(DOCKER_PUSH)')
	+testbench CMD='make check $(call escape_squotes,$(_testbench_vars)) >&2; cat testresults.tap'
.PHONY: testbench-check

docker-build:  ## Build Docker images
	docker build --file local-docker/Dockerfile . -t $(TELEPRESENCE_REGISTRY)/telepresence-local:$(TELEPRESENCE_VERSION)
	docker build k8s-proxy -t $(TELEPRESENCE_REGISTRY)/telepresence-k8s:$(TELEPRESENCE_VERSION) --target telepresence-k8s
	docker build k8s-proxy -t $(TELEPRESENCE_REGISTRY)/telepresence-k8s-priv:$(TELEPRESENCE_VERSION) --target telepresence-k8s-priv
	docker build --file k8s-proxy/Dockerfile.ocp k8s-proxy -t $(TELEPRESENCE_REGISTRY)/telepresence-ocp:$(TELEPRESENCE_VERSION)
.PHONY: docker-build

docker-push: docker-build  ## Push Docker images to TELEPRESENCE_REGISTRY (implies 'docker-build')
	docker push $(TELEPRESENCE_REGISTRY)/telepresence-k8s:$(TELEPRESENCE_VERSION)
	docker push $(TELEPRESENCE_REGISTRY)/telepresence-k8s-priv:$(TELEPRESENCE_VERSION)
	docker push $(TELEPRESENCE_REGISTRY)/telepresence-ocp:$(TELEPRESENCE_VERSION)
	docker push $(TELEPRESENCE_REGISTRY)/telepresence-local:$(TELEPRESENCE_VERSION)
.PHONY: docker-push

VIRTUALENV = PATH=$$PWD/virtualenv/bin:$$PATH
# Presence of __PYENV_LAUNCHER__ in pip processes we launch causes pip
# to write the wrong #! line.  Only expected to be set on OS X.
# https://bugs.python.org/issue22490
PIP = $(VIRTUALENV) env -u __PYENV_LAUNCHER__ pip
DIRFAIL = { r=$$?; rm -rf $@; exit $$r; }
virtualenv: dev-requirements.txt k8s-proxy/requirements.txt  ## Set up Python3 virtual environment for development
	rm -rf $@ || true
	python3 -m venv $@ || $(DIRFAIL)
	$(PIP) install -q -U pip || $(DIRFAIL)
	$(PIP) install -q -r dev-requirements.txt || $(DIRFAIL)
	$(PIP) install -q -r k8s-proxy/requirements.txt || $(DIRFAIL)
	$(PIP) install -q setuptools_scm || $(DIRFAIL) # Ensure subsequent line executes without error on macos
	$(PIP) install -q git+https://github.com/datawire/sshuttle.git@telepresence || $(DIRFAIL)
	$(PIP) install -q --no-use-pep517 -e . || $(DIRFAIL)

lint: virtualenv  ## Run the linters used by CI (implies 'virtualenv')
	./tools/license-check
	$(VIRTUALENV) yapf -dr telepresence packaging tests
	$(VIRTUALENV) flake8 --isolated local-docker k8s-proxy telepresence setup.py packaging tests
	$(VIRTUALENV) mypy --strict-optional telepresence local-docker/entrypoint.py packaging/*.py
	$(VIRTUALENV) mypy --ignore-missing-imports k8s-proxy
	$(VIRTUALENV) telepresence --help
.PHONY: lint

#

format: virtualenv  ## Format source code in-place
	$(VIRTUALENV) yapf -ir telepresence packaging tests
.PHONY: format

#

docs: ## Builds documentation
	@exec docs/build-website.sh
.PHONY: docs

docs-serve: docs ## Serves documentation under localhost for easy editing with preview
	@exec npm start --prefix docs/
.PHONY: docs

#

help:  ## Show this message
	@echo 'usage: make [TARGETS...] [VARIABLES...]'
	@echo
	@echo VARIABLES:
	@sed -n '/[?]=/s/^/  /p' ${MAKEFILE_LIST}
	@echo
	@echo TARGETS:
	@sed -n 's/:.*[#]#/:#/p' ${MAKEFILE_LIST} | column -t -c 2 -s ':#' | sed 's/^/  /'
.PHONY: help

# I put this as the last line in the file because it confuses Emacs
# syntax highlighting and makes the remainder of the file difficult to
# edit.
escape_squotes = $(subst ','\'',$1)
