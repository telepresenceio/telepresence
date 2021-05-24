# Copyright 2020 Datawire. All rights reserved.
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

TELEPRESENCE_REGISTRY ?= docker.io/datawire
DOCKER_PUSH           ?= docker-push

_TELEPRESENCE_VERSION := $(shell unset GOOS GOARCH; go run ./build/genversion.go)
TELEPRESENCE_VERSION ?= $(_TELEPRESENCE_VERSION)
$(if $(filter v2.%,$(TELEPRESENCE_VERSION)),\
  $(info Building TELEPRESENCE_VERSION=$(TELEPRESENCE_VERSION)),\
  $(error TELEPRESENCE_VERSION variable is invalid: It must be a v2.* string, but is '$(TELEPRESENCE_VERSION)'))

default: help
.PHONY: default
.SECONDARY:
.DELETE_ON_ERROR:

include build/tools.mk
include build/go.mk
include build/support.mk

.PHONY: prepare-release
prepare-release: ## (Release) Update nescessary files and tag the release (does not push)
	sed -i.bak "/^### $(patsubst v%,%,$(TELEPRESENCE_VERSION)) (TBD)\$$/s/TBD/$$(date +'%B %-d, %Y')/" CHANGELOG.md
	rm -f CHANGELOG.md.bak
	go mod edit -require=github.com/telepresenceio/telepresence/rpc/v2@$(TELEPRESENCE_VERSION)
	git add CHANGELOG.md go.mod
	$(if $(findstring -,$(TELEPRESENCE_VERSION)),,cp -a pkg/client/connector/testdata/addAgentToWorkload/cur pkg/client/connector/testdata/addAgentToWorkload/$(TELEPRESENCE_VERSION))
	$(if $(findstring -,$(TELEPRESENCE_VERSION)),,git add pkg/client/connector/testdata/addAgentToWorkload/$(TELEPRESENCE_VERSION))
	# Bump the appVersion in the chart for non-rc tags
	$(if $(findstring -,$(TELEPRESENCE_VERSION)),,sed -i.bak "s/^appVersion:.*$$/appVersion: $(patsubst v%,%,$(TELEPRESENCE_VERSION))/" charts/telepresence/Chart.yaml && rm -f charts/telepresence/Chart.yaml.bak)
	git commit --signoff --message='Prepare $(TELEPRESENCE_VERSION)'
	git tag --annotate --message='$(TELEPRESENCE_VERSION)' $(TELEPRESENCE_VERSION)
	git tag --annotate --message='$(TELEPRESENCE_VERSION)' rpc/$(TELEPRESENCE_VERSION)
