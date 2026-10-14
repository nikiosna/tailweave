# tailweave developer entry points.
#
# The lab runs inside a Lima VM on every host; the repository is mounted at the same path,
# so targets transparently re-run themselves inside the VM. The lab changes routing,
# nftables and network namespaces, and the VM keeps that away from the host.
# NO_VM=1 runs the targets directly on a Linux host instead (CI does this).

VM        ?= tailweave
SCENARIO  ?= base
GO_TEST_FLAGS ?=

NO_VM     ?=

ifeq ($(NO_VM),)
IN_LAB := limactl shell --workdir $(CURDIR) $(VM) --
SH     := $(IN_LAB) bash -lc
else
IN_LAB :=
SH     := bash -c
endif

LAB := $(IN_LAB) lab/scripts

.PHONY: help vm-up vm-down vm-destroy sh lab-up lab-status lab-reset playground-up playground-down \
	run lab-login ui ui-check ui-tunnel build test test-lab lint fmt

help: ## Show targets
	@grep -hE '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-16s %s\n", $$1, $$2}'

# --- VM --------------------------------------------------------------------

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

run: build ## Run the tailweave daemon (then make lab-login; control it with sudo bin/tailweave ...)
	$(IN_LAB) sudo bin/tailweave run -config lab/tailweave.json

lab-login: build ## Log `make run` into both lab tailnets with their preauth keys
	$(LAB)/login.sh

ui: build ## Web UI for `make run` on 127.0.0.1:5250 in the VM
	$(IN_LAB) sudo bin/tailweave ui

ui-check: ## Click through the UI in headless Chromium, screenshots in lab/.state/ui
	$(SH) 'lab/scripts/ui-check.sh'

ui-tunnel: ## Make the UI of `make ui` reachable at http://127.0.0.1:5250 on the host
	ssh -F $(HOME)/.lima/$(VM)/ssh.config -N -L 5250:127.0.0.1:5250 lima-$(VM)

# --- Go --------------------------------------------------------------------

build: ## Build binaries into ./bin
	$(SH) 'go build -o bin/ ./cmd/...'

test: ## Unit tests
	$(SH) 'go test $(GO_TEST_FLAGS) ./...'

test-lab: ## Integration tests against the running lab (make lab-up first)
	$(SH) 'go test -tags lab -count=1 -p 1 $(GO_TEST_FLAGS) ./...'

lint: ## go vet + golangci-lint (if installed)
	$(SH) 'go vet ./... && (command -v golangci-lint >/dev/null && golangci-lint run || true)'

fmt: ## gofmt
	$(SH) 'gofmt -w $$(git ls-files "*.go")'
