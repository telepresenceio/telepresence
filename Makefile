# Copyright 2020-2021 Datawire.  All rights reserved.
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
TELEPRESENCE_VERSION ?= $(shell unset GOOS GOARCH; go run ./build-aux/genversion)
# Ensure that the variable is fully expanded. We don't want to call genversion repeatedly
# as it may produce different results every time.
TELEPRESENCE_VERSION := ${TELEPRESENCE_VERSION}

SHELL:=$(shell which bash)

$(if $(filter v2.%,$(TELEPRESENCE_VERSION)),\
  $(info [make] TELEPRESENCE_VERSION=$(TELEPRESENCE_VERSION)),\
  $(error TELEPRESENCE_VERSION variable is invalid: It must be a v2.* string, but is '$(TELEPRESENCE_VERSION)'))
export TELEPRESENCE_VERSION

.DEFAULT_GOAL = help

include build-aux/prelude.mk
include build-aux/tools.mk
include build-aux/main.mk
include build-aux/docs.mk
