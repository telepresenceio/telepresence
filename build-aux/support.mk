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

TELEPRESENCE_BASE_VERSION := $(firstword $(shell shasum base-image/Dockerfile))
.PHONY: base-image
base-image: base-image/Dockerfile ## (ZSupport) Rebuild the base image
	if ! docker pull $(TELEPRESENCE_REGISTRY)/tel2-base:$(TELEPRESENCE_BASE_VERSION); then \
	  cd base-image && docker build --pull -t $(TELEPRESENCE_REGISTRY)/tel2-base:$(TELEPRESENCE_BASE_VERSION) . && \
	  docker push $(TELEPRESENCE_REGISTRY)/tel2-base:$(TELEPRESENCE_BASE_VERSION); \
	fi

.PHONY: help
help:  ## (ZSupport) Show this message
	@echo 'Usage: [VARIABLE=VALUE...] $(MAKE) [TARGETS...]'
	@echo
	@echo VARIABLES:
	@{ $(foreach varname,$(shell sed -n '/[?]=/{ s/[ ?].*//; s/^/  /; p; }' $(sort $(abspath $(MAKEFILE_LIST)))),printf '%s = %s\n' '$(varname)' '$($(varname))';) } | column -t | sed 's/^/  /'
	@echo
	@echo TARGETS:
	@sed -En 's/^([^:]*):[^#]*## *(\([^)]*\))? */\2	\1	/p' $(sort $(abspath $(MAKEFILE_LIST))) | sed 's/^	/($(or $(NAME),this project))&/' | column -t -s '	' | sed 's/^/  /' | sort
	@echo
	@echo "See DEVELOPING.md for more information"
