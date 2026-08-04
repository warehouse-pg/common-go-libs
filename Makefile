all: depend test

SHELL := /bin/bash
.DEFAULT_GOAL := all

# The three docker-image linters this used to shell out to (unibeautify/goimports,
# cytopia/gofmt, golangci/golangci-lint) are all subsumed by golangci-lint v2:
# gofmt and goimports run as its formatters, configured in .golangci.yml.
GOLANGCI_LINT_VERSION := v2.11.4

GOFLAGS :=

# The packages with unit test suites. Kept explicit rather than using `./...` so
# that a package losing its suite is a visible change here.
UNIT_PACKAGES := \
	cluster \
	conv \
	dbconn \
	gperror \
	gplog \
	iohelper \
	structmatcher

.PHONY: all test lint fmt vet unit coverage depend clean tools

test: lint vet unit

lint:
	golangci-lint run

# Rewrites files in place, rather than just reporting, for local use.
fmt:
	golangci-lint fmt

vet:
	go vet ./...

# ginkgo is pinned by the `tool` directive in go.mod, so `go tool` runs the
# version this module was tested against instead of whatever @latest resolves to.
unit:
	go tool ginkgo -r --keep-going --randomize-suites --randomize-all \
		$(UNIT_PACKAGES) \
		2>&1

coverage:
	@./show_coverage.sh

depend:
	go mod download

# golangci-lint is intentionally not a `tool` directive: it does not support
# being built as a library dependency.
tools:
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

clean:
	# Test artifacts
	rm -rf /tmp/go-build*
	rm -rf /tmp/gexec_artifacts*
	rm -rf /tmp/ginkgo*
	# Code coverage files
	rm -rf /tmp/cover*
	rm -rf /tmp/unit*
