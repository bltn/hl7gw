GOLANGCI_LINT ?= go tool golangci-lint
VACUUM ?= go tool vacuum
FUZZTIME ?= 10s
CONFIG ?= hl7gw.json
FUZZ_PKGS = ./internal/hl7 ./internal/mllp
DIST_TARGETS = linux:amd64:Linux_x86_64 linux:arm64:Linux_aarch64 darwin:amd64:Darwin_x86_64 darwin:arm64:Darwin_arm64

.PHONY: run
run:
	go run . run -config $(CONFIG)

.PHONY: configure
configure:
	go run . configure -output $(CONFIG)

.PHONY: build
build:
	go build -o bin/hl7gw .

.PHONY: dist
dist:
	rm -rf dist && mkdir -p dist
	set -e; for t in $(DIST_TARGETS); do \
		set -- $$(echo $$t | tr : ' '); \
		dir=dist/hl7gw_$$3; mkdir -p $$dir; \
		CGO_ENABLED=0 GOOS=$$1 GOARCH=$$2 go build -trimpath -o $$dir/hl7gw .; \
		cp LICENSE README.md $$dir/; \
		tar -czf $$dir.tar.gz -C $$dir hl7gw LICENSE README.md; \
		rm -rf $$dir; \
	done
	cd dist && shasum -a 256 *.tar.gz > checksums.txt

.PHONY: unit_tests
unit_tests:
	go test -race $(UNIT_FLAGS) ./... $(UNIT_ARGS)

.PHONY: e2e_tests
e2e_tests:
	$(E2E_ENV) go test -v -race -count=1 -parallel $$(( 2 * $$(getconf _NPROCESSORS_ONLN) )) -tags e2e ./e2e/...

.PHONY: run_all_tests_with_coverage
run_all_tests_with_coverage:
	rm -rf coverage && mkdir -p coverage/unit coverage/e2e coverage/merged
	$(MAKE) unit_tests UNIT_FLAGS="-cover -coverpkg=./..." UNIT_ARGS="-args -test.gocoverdir=$(CURDIR)/coverage/unit"
	$(MAKE) e2e_tests E2E_ENV="GOCOVERDIR=$(CURDIR)/coverage/e2e"
	go tool covdata merge -i=coverage/unit,coverage/e2e -o=coverage/merged
	go tool covdata textfmt -i=coverage/merged -o=cover.out
	go tool cover -func=cover.out
	go tool cover -func=cover.out | grep -q '^total:.*100.0%$$' || { echo "coverage below 100%"; exit 1; }

.PHONY: fuzz
fuzz:
	set -e; for pkg in $(FUZZ_PKGS); do \
		for target in $$(go test -list '^Fuzz' $$pkg | grep '^Fuzz'); do \
			go test -run '^$$' -fuzz "^$$target\$$" -fuzztime $(FUZZTIME) $$pkg; \
		done; \
	done

.PHONY: fmt
fmt:
	$(GOLANGCI_LINT) fmt

.PHONY: vet
vet:
	go vet ./...

.PHONY: tidy
tidy:
	go mod tidy

.PHONY: lint
lint:
	$(GOLANGCI_LINT) run --build-tags e2e
	$(VACUUM) lint -r vacuum-ruleset.yaml openapi.yaml

.PHONY: check
check: lint run_all_tests_with_coverage fuzz

.PHONY: clean
clean:
	rm -rf bin dist
