GO ?= go
VERSION := $(shell cat VERSION)
PREFIX ?= $(HOME)/.local

.PHONY: build test vet check dist install
build:
	CGO_ENABLED=0 $(GO) build -trimpath -buildvcs=false -ldflags="-s -w -X main.version=$(VERSION)" -o hop .
test:
	$(GO) test -race -timeout 120s ./...
vet:
	$(GO) vet ./...
check: test vet
	@test -z "$$(gofmt -l .)" || (echo 'Run gofmt -w on Go sources'; exit 1)
dist:
	python3 scripts/release.py
install: build
	install -d "$(DESTDIR)$(PREFIX)/bin"
	install -m 755 hop "$(DESTDIR)$(PREFIX)/bin/hop"
