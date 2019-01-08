ifeq ($(words $(filter $(abspath $(lastword $(MAKEFILE_LIST))),$(abspath $(MAKEFILE_LIST)))),1)
include $(dir $(lastword $(MAKEFILE_LIST)))kubernaut-ui.mk
include $(dir $(lastword $(MAKEFILE_LIST)))kubeapply.mk

PROFILE?=dev

IMAGE_VARS=$(filter %_IMAGE,$(.VARIABLES))
IMAGES=$(foreach var,$(IMAGE_VARS),$($(var)))
IMAGE_DEFS=$(foreach var,$(IMAGE_VARS),$(var)=$($(var))$(NL))
IMAGE_DEFS_SH="$(subst $(SPACE),\n,$(foreach var,$(IMAGE_VARS),$(var)=$($(var))))\n"
MANIFESTS?=$(wildcard k8s/*.yaml)

env:
	$(eval $(subst @NL,$(NL), $(shell go run build-aux/env.go -profile $(PROFILE) -newline "@NL" -input config.json)))
.PHONY: env

hash: ## Show the computed version hash. The hash is based on non gitignored files.
hash: env
	@echo HASH=$(HASH)
.PHONY: hash

push_ok: env
	@if [ "$(PROFILE)" == "prod" ]; then echo "CANNOT PUSH TO PROD"; exit 1; fi
.PHONY: push_ok

push: ## Build docker images, tag them with the computed hash, and push them to the docker repo specified in config.json.
push: push_ok docker
	@for IMAGE in $(IMAGES); do \
		docker push $${IMAGE}; \
	done
	printf $(IMAGE_DEFS_SH) > pushed.txt
.PHONY: push

apply: ## Apply the most recently pushed images. (This is useful for quickly deploying yaml only changes without rebuilding & pushing containers.)
apply: $(KUBECONFIG) $(KUBEAPPLY)
	KUBECONFIG=$(KUBECONFIG) $(sort $(shell cat pushed.txt)) $(KUBEAPPLY) $(MANIFESTS:%=-f %)
.PHONY: apply

deploy: ## Shorthand for `make push apply`.
deploy: push apply
.PHONY: deploy

endif
