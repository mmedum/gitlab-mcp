# Common development tasks. `make parity` holds `check` against
# .github/workflows/ci.yml; this comment does not.

GO        ?= go
# .exe on Windows, where a file without one cannot be executed.
EXE       := $(if $(filter Windows_NT,$(OS)),.exe,)
BIN       ?= ./gitlab-mcp$(EXE)
VERSION   ?= dev
PKG        = github.com/mmedum/gitlab-mcp
LDFLAGS    = -s -w -X $(PKG)/internal/version.Version=$(VERSION)
# The packages coverage is measured over, cmd/ included. CI runs
# `make cover`, so this is the only place the list is written.
COVERPKG   = ./cmd/...,./internal/...
# Where the release's binaries are, what version the bundle claims, and
# where it lands. Only `mcpb-pack` reads them; `gates release` holds
# MCPB_OUT against the path in .goreleaser.yaml.
DIST      ?= dist
MCPB_OUT  ?= dist/gitlab-mcp_$(VERSION).mcpb
# The schema dump the description and body gates read, and CI uploads.
SCHEMAS   ?= schemas.json
# A pull request is measured from the merge-base of its head and the
# base branch as it is now, never from the base SHA the event recorded,
# which is stale in stacked pull requests. `gates merge-base` computes it.
PR_BASE_REF ?= origin/main
PR_HEAD     ?= HEAD

# The repository's own checks: one binary, built once per make run.
GATES     ?= ./.gates$(EXE)

# Every tool pinned and fetched the way CI fetches it, never whatever is
# on the PATH. A distribution's golangci-lint built with an older Go
# refuses this module and reports it as "can't load config". `make pins`
# holds each version here against the one in the workflows.
GOLANGCI_LINT ?= github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0
GOVULNCHECK   ?= golang.org/x/vuln/cmd/govulncheck@v1.8.0
# v1.6.0 until docs/architecture.md §17.6 settles v1 against v2.
GOLICENSES    ?= github.com/google/go-licenses@v1.6.0
# The module path is zricethezav: the project moved organization and the
# module path did not follow.
GITLEAKS      ?= github.com/zricethezav/gitleaks/v8@v8.30.1
ACTIONLINT    ?= github.com/rhysd/actionlint/cmd/actionlint@v1.7.12
# release.yml runs goreleaser through goreleaser-action, which installs
# its own copy at GORELEASER_VERSION. A rehearsal on a different
# goreleaser is not a rehearsal.
GORELEASER    ?= github.com/goreleaser/goreleaser/v2@v2.18.2

.PHONY: all
all: check

# The definition of done. `make checklist` holds this list against
# CLAUDE.md, both ways.
.PHONY: check
check: fmt vet tidy lint cover vuln licenses secrets leaks pins classes api-coverage api-fields schema-diff descriptions bodies smoke staleness checklist changelog-links transcript live-cover outcomes evals-check mcpb release server-json actionlint goreleaser-check parity

# --- build ---------------------------------------------------------------

.PHONY: build
build: ## Build the binary
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN) ./cmd/gitlab-mcp

.PHONY: install
install: ## go install the binary
	CGO_ENABLED=0 $(GO) install -trimpath -ldflags="$(LDFLAGS)" ./cmd/gitlab-mcp

.PHONY: gates
gates: ## Build the repository's own checks
	$(GO) build -o $(GATES) ./scripts/gates

.PHONY: schemas
schemas: build ## Dump the tool and resource schemas, every flag and toolset on
	$(BIN) --dump-schemas > $(SCHEMAS)

# --- go hygiene ----------------------------------------------------------

.PHONY: fmt
fmt: ## Fail if gofmt would change anything
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt would change:"; echo "$$out"; exit 1; fi

.PHONY: vet
vet: ## go vet, the tagged code included so it keeps compiling
	$(GO) vet ./...
	$(GO) vet -tags=live ./...
	$(GO) vet -tags=evals ./...

.PHONY: tidy
tidy: ## go.mod and go.sum are what `go mod tidy` would write
	$(GO) mod tidy -diff

.PHONY: lint
lint:
	$(GO) run $(GOLANGCI_LINT) run

.PHONY: test
test: ## Unit tests with the race detector and a coverage profile
	$(GO) test -race -shuffle=on -coverpkg=$(COVERPKG) -coverprofile=cov.out -covermode=atomic ./...

.PHONY: cover
cover: test gates ## The per-package coverage floors
	$(GATES) coverage cov.out

.PHONY: vuln
vuln:
	$(GO) run $(GOVULNCHECK) ./...

.PHONY: licenses
licenses:
	$(GO) run $(GOLICENSES) check ./... --allowed_licenses=Apache-2.0,BSD-2-Clause,BSD-3-Clause,MIT,ISC

.PHONY: secrets
secrets: ## Credentials in the tree and in every commit the clone holds
	$(GO) run $(GITLEAKS) dir . --config .gitleaks.toml --redact --no-banner
	$(GO) run $(GITLEAKS) git . --config .gitleaks.toml --redact --no-banner

.PHONY: actionlint
actionlint: ## The workflows are valid
	$(GO) run $(ACTIONLINT)

.PHONY: goreleaser-check
goreleaser-check: ## The release config is valid
	$(GO) run $(GORELEASER) check

# --- gates ---------------------------------------------------------------

.PHONY: leaks
leaks: gates ## Identifiers and instance content in the working tree
	$(GATES) leaks

.PHONY: pins
pins: gates ## Actions pinned by SHA, tool versions exact and equal
	$(GATES) pins

.PHONY: classes
classes: gates ## The error vocabulary, held closed from both sides
	$(GATES) classes

.PHONY: api-coverage
api-coverage: gates ## Every published API operation has a verdict
	$(GATES) api-coverage

.PHONY: api-fields
api-fields: gates ## Every field sent or decoded exists in the published API
	$(GATES) api-fields

.PHONY: schema-diff
schema-diff: schemas gates ## The tool surface against the last tag, else the recorded baseline
	$(GATES) schema-diff $(BIN)

.PHONY: descriptions
descriptions: schemas gates ## Every tool and input is described, witnesses included
	$(GATES) descriptions $(SCHEMAS)

.PHONY: bodies
bodies: schemas gates ## Every written string goes through the quick-action guard or is listed
	$(GATES) bodies $(SCHEMAS)

.PHONY: smoke
smoke: build gates ## Drive the binary over stdio
	$(GATES) smoke $(BIN)

.PHONY: staleness
staleness: build gates ## The docs match the code
	$(GATES) staleness $(BIN)

.PHONY: checklist
checklist: gates ## CLAUDE.md's definition of done against `check`
	$(GATES) checklist

.PHONY: changelog-links
changelog-links: gates ## Every CHANGELOG version heading has its link reference
	$(GATES) changelog-links

.PHONY: transcript
transcript: gates ## The drivers print only through their redactor
	$(GATES) transcript

.PHONY: live-cover
live-cover: build gates ## Every tool option is driven live or waived with a reason
	$(GATES) live-cover $(BIN)

.PHONY: outcomes
outcomes: gates ## Every write's result states its outcome
	$(GATES) outcomes

# The deterministic half of the evals: every scorer must fail on an
# instance nobody touched. No model, no key.
.PHONY: evals-check
evals-check: ## The eval scorers discriminate, with no model and no key
	$(GO) run -tags=evals ./scripts/evals -self-check

.PHONY: mcpb
mcpb: gates ## The bundle manifest describes the bundle the packer builds
	$(GATES) mcpb

.PHONY: release
release: gates ## The release config builds, signs and uploads what the packer stages
	$(GATES) release

.PHONY: server-json
server-json: gates ## The registry entry generator holds the registry's rules
	$(GATES) server-json

.PHONY: parity
parity: gates ## `check` and ci.yml run the same things
	$(GATES) parity

# --- pull requests (CI's pr job; not in `check`) -------------------------

.PHONY: merge-base
merge-base: gates ## Print the commit a pull request is measured from
	$(GATES) merge-base $(PR_BASE_REF) $(PR_HEAD)

.PHONY: changelog
changelog: gates ## A pull request adds a CHANGELOG entry, unless it cuts a release
	$(GATES) changelog $(PR_BASE_REF) $(PR_HEAD)

# A changed tool surface needs `SCHEMA-CHANGE:` (additive) or
# `BREAKING CHANGE:` on some commit the pull request adds, as an empty
# commit rather than an amend.
.PHONY: schema-ack
schema-ack: build gates ## A changed tool surface is acknowledged in a commit
	$(GATES) schema-ack $(BIN) $(PR_BASE_REF) $(PR_HEAD)

# --- manual --------------------------------------------------------------

.PHONY: schema-baseline
schema-baseline: build gates ## Record the current tool surface as the baseline (deliberate; manual)
	$(GATES) schema-baseline $(BIN)

.PHONY: leaks-history
leaks-history: gates ## Every blob, commit message and tag in the history (manual)
	$(GATES) leaks-history

.PHONY: api-diff
api-diff: gates ## Refetch the OpenAPI snapshot at a GitLab tag (network; manual)
	$(GATES) api-diff $(API_TAG)

.PHONY: schema-refetch
schema-refetch: gates ## The vendored schemas against what their sources serve (network; manual)
	$(GATES) schema-refetch

.PHONY: deps
deps: gates ## Direct dependencies updated within six months or pinned with a reason (network; manual)
	$(GATES) deps

# Run from the universal binary's post hook in .goreleaser.yaml, which
# calls `go run ./scripts/gates` directly. Not in `check`: it needs the
# built binaries.
.PHONY: mcpb-pack
mcpb-pack: gates ## Pack the .mcpb from a built dist tree (release; manual)
	$(GATES) mcpb-pack $(DIST) $(VERSION) $(MCPB_OUT)

.PHONY: release-notes
release-notes: gates ## Print the CHANGELOG section a tag would publish (manual)
	$(GATES) release-notes $(VERSION)

# As far as a laptop can take the release. It skips what needs the OIDC
# token only a workflow run has, and the SBOMs, which need syft. It also
# skips goreleaser's dirty-tree check; docs/release.md says what that costs.
.PHONY: release-rehearse
release-rehearse: ## Build the whole release locally, unsigned (manual)
	$(GO) run $(GORELEASER) release --snapshot --clean --skip=publish,sign,sbom

.PHONY: live
live: build ## Drive the built binary against a scratch project on gitlab.com (docs/development.md)
	$(GO) run -tags=live ./scripts/livegitlab -bin $(BIN) $(LIVE_ARGS)

# Not in `check`: it spends money and is not deterministic. Its
# transcript is read like the live driver's.
.PHONY: evals
evals: build ## Score a model against the tool surface (needs the claude CLI signed in; manual)
	$(GO) run -tags=evals ./scripts/evals -bin $(BIN) $(EVAL_ARGS)

.PHONY: hooks
hooks: ## Point git at .githooks
	git config core.hooksPath .githooks

.PHONY: clean
clean:
	$(RM) $(BIN) $(GATES) cov.out $(SCHEMAS)
	$(RM) -r $(DIST)
