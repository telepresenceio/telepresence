# Depends on shell.mk and kubeapply.mk

PROFILE?=dev

IMAGE_VARS=$(filter %_IMAGE,$(.VARIABLES))
IMAGES=$(foreach var,$(IMAGE_VARS),$($(var)))
IMAGE_DEFS=$(foreach var,$(IMAGE_VARS),$(var)=$($(var))$(NL))
IMAGE_DEFS_SH="$(subst $(SPACE),\n,$(foreach var,$(IMAGE_VARS),$(var)=$($(var))))\n"
MANIFESTS?=$(wildcard k8s/*.yaml)

env: ## ???
	$(eval $(subst @NL,$(NL), $(shell go run build-aux/env.go -profile $(PROFILE) -newline "@NL" -input config.json)))
.PHONY: env

hash: ## ???
hash: env
	@echo HASH=$(HASH)
.PHONY: hash

push_ok: ## ???
push_ok: env
	@if [ "$(PROFILE)" == "prod" ]; then echo "CANNOT PUSH TO PROD"; exit 1; fi
.PHONY: push_ok

blah: ## ???
blah: env
	@echo '$(IMAGES)'
	@echo '$(IMAGE_DEFS)'
.PHONY: blah

push: ## Docker push
push: push_ok docker
	@for IMAGE in $(IMAGES); do \
		docker push $${IMAGE}; \
	done
	printf $(IMAGE_DEFS_SH) > pushed.txt
.PHONY: push

apply: ## ???
apply: $(CLUSTER) $(KUBEAPPLY)
	KUBECONFIG=$(CLUSTER) $(sort $(shell cat pushed.txt)) $(KUBEAPPLY) $(MANIFESTS:%=-f %)
.PHONY: apply

deploy: ## ???
deploy: push apply
.PHONY: deploy
