.PHONY: all lint vet build test clean schema-update docs

BINARY := adolint
GOBIN  := $(shell go env GOPATH)/bin
SCHEMA_URL := https://raw.githubusercontent.com/microsoft/azure-pipelines-vscode/main/service-schema.json

all: lint vet test build

lint:
	$(GOBIN)/golangci-lint run ./...

vet:
	go vet ./...

build:
	go build -o $(BINARY) ./cmd/adolint

test:
	go test -race -count=1 ./...

clean:
	rm -f $(BINARY)

# Regenerate the rule pages in docs/rules from the rule registry (checked by go test ./internal/docs).
docs:
	go run ./cmd/gendocs docs

# Refresh the embedded Azure Pipelines schema from microsoft/azure-pipelines-vscode (MIT).
schema-update:
	curl -fsSL $(SCHEMA_URL) -o internal/schema/service-schema.json
