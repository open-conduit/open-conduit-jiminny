# The package directory Open Conduit installs: the binary, its manifest and
# its certification fixture.
PACKAGE := dist/jiminny
VERSION ?= 0.1.0
LDFLAGS := -s -w -X github.com/open-conduit/open-conduit-jiminny/jiminny.Version=$(VERSION)
# The Open Conduit host to certify against: the release the SDK is pinned to.
HOST_VERSION := $(shell go list -m -f '{{.Version}}' github.com/open-conduit/open-conduit)
HOST := bin/open-conduit

.PHONY: check test package host certify config-check clean

check: test package certify config-check

test:
	gofmt -l . | (! grep .)
	go vet ./...
	go test -race -count=1 ./...

package:
	rm -rf $(PACKAGE) && mkdir -p $(PACKAGE)
	go build -trimpath -ldflags '$(LDFLAGS)' -o $(PACKAGE)/jiminny-extension ./cmd/jiminny-extension
	$(PACKAGE)/jiminny-extension package-manifest > $(PACKAGE)/open-conduit-extension.json
	$(PACKAGE)/jiminny-extension package-certification > $(PACKAGE)/certification.json

host:
	GOBIN=$(CURDIR)/bin go install github.com/open-conduit/open-conduit/cmd/open-conduit@$(HOST_VERSION)

certify: package host
	$(HOST) extension inspect $(PACKAGE)
	$(HOST) extension certify $(PACKAGE) --report dist/certification-report.json

# The example config, checked offline with the package installed.
config-check: package host
	rm -rf dist/config && mkdir -p dist/config
	cp examples/config.json dist/config/config.json
	$(HOST) extension install --config dist/config/config.json $(PACKAGE)
	$(HOST) config check --file dist/config/config.json

clean:
	rm -rf dist bin
