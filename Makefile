SHELL := /bin/bash

GOLANGCI_LINT_VERSION ?= v2.14.0
GOLANGCI_LINT ?= docker run --rm -t \
	-v $(CURDIR):/app -w /app \
	-v $(shell go env GOMODCACHE):/go/pkg/mod \
	-v $(HOME)/.cache/golangci-lint:/root/.cache \
	golangci/golangci-lint:$(GOLANGCI_LINT_VERSION) golangci-lint
GOVULNCHECK_VERSION ?= v1.8.0
COMPOSE ?= docker compose
INFRA_SERVICES := postgres keycloak ministack

.PHONY: up down infra-up fmt fmt-check lint vet vuln tidy-check go-version-check test test-integration check

up:
	$(COMPOSE) up --build

down:
	$(COMPOSE) down -v

# aws-init is one-shot: it runs once ministack is healthy.
infra-up:
	$(COMPOSE) up -d --wait $(INFRA_SERVICES)
	$(COMPOSE) up aws-init --exit-code-from aws-init

fmt:
	$(GOLANGCI_LINT) fmt

fmt-check:
	@test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }
	@$(GOLANGCI_LINT) fmt --diff

lint:
	$(GOLANGCI_LINT) run

vet:
	go vet ./...
	go vet -tags=integration,e2e,faultinject ./...

vuln:
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

tidy-check:
	go mod tidy -diff

go-version-check:
	@mod=$$(awk '/^go /{print $$2}' go.mod); \
	img=$$(sed -nE 's/^FROM golang:([0-9.]+)-alpine.*/\1/p' Dockerfile); \
	test "$$mod" = "$$img" || { echo "go.mod ($$mod) != Dockerfile ($$img)"; exit 1; }

test:
	go test -race ./...

test-integration: infra-up
	go test -tags=integration -race -count=1 ./...

check: fmt-check lint vet tidy-check go-version-check test
