GO ?= go
VERSION := $(shell cat VERSION)
PREFIX ?= $(HOME)/.local

.PHONY: build test test-install vet check dist install
build:
	CGO_ENABLED=0 $(GO) build -trimpath -buildvcs=false -ldflags="-s -w -X main.version=$(VERSION)" -o hop ./cmd/hop
test:
	$(GO) test -race -timeout 120s ./...
test-install:
	python3 -m unittest discover -s scripts/tests -p 'test_install.py'
vet:
	$(GO) vet ./...
check: test vet test-install
	@test -z "$$(gofmt -l .)" || (echo 'Run gofmt -w on Go sources'; exit 1)
dist:
	python3 scripts/release.py
install: build
	install -d "$(DESTDIR)$(PREFIX)/bin"
	install -m 755 hop "$(DESTDIR)$(PREFIX)/bin/hop"
