# Ensure that go environment prerequisites are met
GOPATH=$(shell go env GOPATH)
GOHOSTOS=$(shell go env GOHOSTOS)
GOHOSTARCH=$(shell go env GOHOSTARCH)

# Install protoc-gen and protoc-gen-go-grpc
$(GOPATH)/bin/protoc-gen-go $(GOPATH)/bin/protoc-gen-go-grpc:
	go get github.com/golang/protobuf/protoc-gen-go google.golang.org/grpc/cmd/protoc-gen-go-grpc

.PHONY: protoc-tools
protoc-tools: go.mod $(GOPATH)/bin/protoc-gen-go $(GOPATH)/bin/protoc-gen-go-grpc

# proto/gRPC generation using protoc
pkg/%.pb.go pkg/%_grpc.pb.go: %.proto
	protoc --proto_path=. --go_out=. --go-grpc_out=. $<

TP_RPC_FILES = pkg/rpc/daemon.pb.go pkg/rpc/daemon_grpc.pb.go

.PHONY: generate
generate: protoc-tools $(TP_RPC_FILES)

.PHONY: generate-clean
generate-clean:
	rm -rf pkg/rpc/*

# pkg sources excluding rpc
TP_PKG_SOURCES = $(shell find pkg -type f -name '*.go' | grep -v '/testdata/' | grep -v '_test.go' | grep -v '/rpc/')

bin_$(GOHOSTOS)_$(GOHOSTARCH)/telepresence: main.go $(TP_PKG_SOURCES) $(TP_RPC_FILES)
	go build -o bin_$(GOHOSTOS)_$(GOHOSTARCH)/telepresence .

.PHONY: build
build: bin_$(GOHOSTOS)_$(GOHOSTARCH)/telepresence

.phony: test
test: telepresence
	go test .

define _help.targets
  $(BLD)$(MAKE) $(BLU)help$(END)            -- displays the main help message.

  $(BLD)$(MAKE) $(BLU)targets$(END)         -- displays this message.

  $(BLD)$(MAKE) $(BLU)clean$(END)           -- cleans built artefacts.

  $(BLD)$(MAKE) $(BLU)generate-clean$(END)  -- delete generated files that get checked in to Git.

  $(BLD)$(MAKE) $(BLU)generate$(END)        -- update generated files that get checked in to Git.

  $(BLD)$(MAKE) $(BLU)lint$(END)            -- runs golangci-lint.

  $(BLD)$(MAKE) $(BLU)build$(END)           -- runs go build

  $(BLD)$(MAKE) $(BLU)clean$(END)           -- runs go test
endef
