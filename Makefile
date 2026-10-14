# tailweave developer entry points.
#
# On macOS the lab runs inside a Lima VM; the repository is mounted at the same path,
# so targets transparently re-run themselves inside the VM. On Linux they run directly.

VM        ?= tailweave
SCENARIO  ?= base
GO_TEST_FLAGS ?=

ifeq ($(shell uname -s),Darwin)
IN_LAB := limactl shell --workdir $(CURDIR) $(VM) --
SH     := $(IN_LAB) bash -lc
else
IN_LAB :=
SH     := bash -c
endif

LAB := $(IN_LAB) lab/scripts

.PHONY: help vm-up vm-down vm-destroy sh lab-up lab-status lab-reset playground-up playground-down \
	build test test-lab lint fmt

help: ## Show targets
	@grep -hE '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-16s %s\n", $$1, $$2}'

# --- VM (macOS only) -------------------------------------------------------

vm-up: ## Create/start the Lima VM
	@if limactl list -q | grep -qx $(VM); then limactl start $(VM); else \
		limactl start --tty=false --name $(VM) \
			--set '.mounts=[{"location":"$(CURDIR)","writable":true}]' lab/lima/tailweave.yaml; fi

vm-down: ## Stop the Lima VM
	limactl stop $(VM)

vm-destroy: ## Delete the Lima VM
	limactl delete --force $(VM)

sh: ## Shell inside the VM, in the repo directory
	limactl shell --workdir $(CURDIR) $(VM)

# --- Lab -------------------------------------------------------------------

lab-up: ## Start headscales + peers (SCENARIO=base|subnets|exit-nodes|dns-suffix|all)
	$(LAB)/bootstrap.sh $(SCENARIO)

lab-status: ## Show nodes in every lab tailnet
	$(LAB)/status.sh

lab-reset: ## Remove all lab containers, volumes and state
	$(LAB)/reset.sh

playground-up: ## Two tailscaled in two netns, one per tailnet (no tailweave)
	$(LAB)/netns-playground.sh up

playground-down: ## Remove playground namespaces
	$(LAB)/netns-playground.sh down

# --- Go --------------------------------------------------------------------

build: ## Build binaries into ./bin
	$(SH) 'go build -o bin/ ./cmd/...'

test: ## Unit tests
	$(SH) 'go test $(GO_TEST_FLAGS) ./...'

test-lab: ## Integration tests against the running lab (make lab-up first)
	$(SH) 'go test -tags lab -count=1 $(GO_TEST_FLAGS) ./...'

lint: ## go vet + golangci-lint (if installed)
	$(SH) 'go vet ./... && (command -v golangci-lint >/dev/null && golangci-lint run || true)'

fmt: ## gofmt
	$(SH) 'gofmt -w $$(git ls-files "*.go")'
