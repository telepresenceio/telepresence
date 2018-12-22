include build-aux/common.mk
include build-aux/go.mk
include build-aux/kubernaut-ui.mk
include build-aux/help.mk

.DEFAULT_GOAL = help

export PATH:=$(CURDIR)/bin_$(GOOS)_$(GOARCH):$(PATH)

test-cluster: $(KUBECONFIG) bin_$(GOOS)_$(GOARCH)/kubeapply
	bin_$(GOOS)_$(GOARCH)/kubeapply -f k8s
.PHONY: test-cluster

# We need to pass special `-exec …` flags to to `go test` for certain
# packages, so disable go.mk's built-in go-test, and define our own.
go.DISABLE_GO_TEST = y
go-test-nat: go-get
	go test -v -exec sudo $(go.module)/internal/pkg/nat
go-test-teleproxy: go-get test-cluster
	go test -v -exec "sudo env PATH=${PATH} KUBECONFIG=${KUBECONFIG}" $(go.module)/cmd/teleproxy
go-test-other: go-get
	go test -v $(filter-out $(go.module)/internal/pkg/nat $(go.module)/cmd/teleproxy,$(go.pkgs))
.PHONY: go-test-nat go-test-teleproxy go-test-other
go-test: go-test-nat go-test-teleproxy go-test-other

check-docker:
ifneq ($(shell which docker 2>/dev/null),)
check-docker: $(addprefix bin_linux_amd64/,$(notdir $(go.bins)))
	docker build -f scripts/Dockerfile . -t teleproxy-make
	docker run --cap-add=NET_ADMIN teleproxy-make go-test-nat
else
	@echo "SKIPPING DOCKER TESTS"
endif
.PHONY: check-docker
check: check-docker

clean: release

# Utility targets

run: build
	bin_$(GOOS)_$(GOARCH)/teleproxy
.PHONY: run
