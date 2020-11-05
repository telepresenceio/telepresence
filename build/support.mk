# Additional targets to help developers

.PHONY: base-image
base-image: ## (ZSupport) Rebuild the base image
	cd base-image && docker build . -t $(TELEPRESENCE_REGISTRY)/tel2-base:$(shell date +%Y%m%d)
	@echo
	@echo "To use this base image, push the image and update .ko.yaml"

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
