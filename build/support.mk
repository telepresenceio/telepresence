# Additional targets to help developers

TELEPRESENCE_BASE_VERSION := $(firstword $(shell shasum base-image/Dockerfile))
.PHONY: base-image
base-image: base-image/Dockerfile ## (ZSupport) Rebuild the base image
	if ! docker pull $(TELEPRESENCE_REGISTRY)/tel2-base:$(TELEPRESENCE_BASE_VERSION); then \
	  cd base-image && docker build --pull -t $(TELEPRESENCE_REGISTRY)/tel2-base:$(TELEPRESENCE_BASE_VERSION) . && \
	  docker push $(TELEPRESENCE_REGISTRY)/tel2-base:$(TELEPRESENCE_BASE_VERSION); \
	fi

.PHONY: help
help:  ## (ZSupport) Show this message
	@echo 'usage: make [TARGETS...] [VARIABLES...]'
	@echo
	@echo VARIABLES:
	@sed -n '/[?]=/s/^/  /p' ${MAKEFILE_LIST}
	@echo
	@echo "TELEPRESENCE_VERSION is $(TELEPRESENCE_VERSION)"
	@echo
	@echo TARGETS:
	@sed -En 's/^([^:]*):[^#]*## *(\([^)]*\))? */\2	\1	/p' $(sort $(abspath $(MAKEFILE_LIST))) | sed 's/^	/($(or $(NAME),this project))&/' | column -t -s '	' | sed 's/^/  /' | sort
	@echo
	@echo "See DEVELOPING.md for more information"
