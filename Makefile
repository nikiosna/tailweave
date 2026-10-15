# tailweave: build and install, plus the developer entry points.
#
#   make                 build ./tailweave for this machine (needs Go)
#   sudo make install    install it with its systemd or launchd service
#   sudo make uninstall  stop the service and remove both again
#
# The lab runs inside a Lima VM on every host; the repository is mounted at the same path,
# so targets transparently re-run themselves inside the VM. The lab changes routing,
# nftables and network namespaces, and the VM keeps that away from the host.
# NO_VM=1 runs the targets directly on a Linux host instead (CI does this).

VM        ?= tailweave
MAC_VM    ?= tailweave-mac
SCENARIO  ?= base
GO_TEST_FLAGS ?=
MAC_TEST_FLAGS ?= -test.v

NO_VM     ?=
VERSION   ?= $(shell git describe --always --dirty 2>/dev/null || echo dev)

PREFIX    ?= /usr/local
DESTDIR   ?=
OS        := $(shell uname -s)
BINDIR    := $(DESTDIR)$(PREFIX)/bin
SYSTEMD_UNIT := $(DESTDIR)/etc/systemd/system/tailweave.service
LAUNCHD_LABEL := com.github.nikiosna.tailweave
LAUNCHD_PLIST := $(DESTDIR)/Library/LaunchDaemons/$(LAUNCHD_LABEL).plist

.DEFAULT_GOAL := all

ifeq ($(NO_VM),)
IN_LAB := limactl shell --workdir $(CURDIR) $(VM) --
SH     := $(IN_LAB) bash -lc
else
IN_LAB :=
SH     := bash -c
endif

LAB := $(IN_LAB) lab/scripts

# The macOS VM is the device under test for the macOS version; the lab stays in $(VM).
MAC := limactl shell --workdir $(CURDIR) $(MAC_VM)
DARWIN_GO := GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go

.PHONY: all install uninstall clean help vm-up vm-down vm-destroy sh mac-vm-up mac-vm-down mac-vm-destroy mac-sh mac-build \
	mac-run mac-login mac-test-lab lab-up lab-status lab-reset playground-up playground-down \
	run lab-login ui ui-check ui-tunnel lab-build test test-lab lint fmt

help: ## Show targets
	@grep -hE '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-16s %s\n", $$1, $$2}'

# --- Build and install -----------------------------------------------------

# These run directly on this machine, never in the VM.
all: ## Build ./tailweave for this machine (then: sudo make install)
	@command -v go >/dev/null || { echo "Go is missing: install Go $$(sed -n 's/^go //p' go.mod) or newer" >&2; exit 1; }
	CGO_ENABLED=0 go build -ldflags "-X main.version=$(VERSION)" -o tailweave ./cmd/tailweave

# install does not build, so sudo never runs Go and never writes into the checkout.
install: ## Install ./tailweave (into /usr/local/bin, PREFIX=...) and its systemd/launchd service
	@test -x tailweave || { echo "./tailweave does not exist yet: run make first" >&2; exit 1; }
	install -d $(BINDIR)
	install -m 755 tailweave $(BINDIR)/tailweave
ifeq ($(OS),Darwin)
	install -d $(dir $(LAUNCHD_PLIST))
	sed 's|/usr/local/bin/tailweave|$(PREFIX)/bin/tailweave|' contrib/launchd/$(LAUNCHD_LABEL).plist > $(LAUNCHD_PLIST)
	chmod 644 $(LAUNCHD_PLIST)
	@if [ -z "$(DESTDIR)" ] && launchctl print system/$(LAUNCHD_LABEL) >/dev/null 2>&1; then \
		echo; echo "Installed. The service still runs the old version, restart it:"; \
		echo "  sudo launchctl kickstart -k system/$(LAUNCHD_LABEL)"; \
	else \
		echo; echo "Installed. Start the service (and at every boot):"; \
		echo "  sudo launchctl bootstrap system $(LAUNCHD_PLIST:$(DESTDIR)%=%)"; \
		echo "Logs: /var/log/tailweave.log. Then add a tailnet: sudo tailweave login <name>"; \
	fi
else
	install -d $(dir $(SYSTEMD_UNIT))
	sed 's|/usr/local/bin/tailweave|$(PREFIX)/bin/tailweave|' contrib/systemd/tailweave.service > $(SYSTEMD_UNIT)
	chmod 644 $(SYSTEMD_UNIT)
	@if [ -z "$(DESTDIR)" ] && command -v systemctl >/dev/null; then systemctl daemon-reload; fi
	@if [ -z "$(DESTDIR)" ] && systemctl is-active --quiet tailweave 2>/dev/null; then \
		echo; echo "Installed. The service still runs the old version, restart it:"; \
		echo "  sudo systemctl restart tailweave"; \
	else \
		echo; echo "Installed. Start the service (and at every boot):"; \
		echo "  sudo systemctl enable --now tailweave"; \
		echo "Logs: journalctl -u tailweave -f. Then add a tailnet: sudo tailweave login <name>"; \
	fi
endif

# Stopping the service makes tailweave remove its routes and DNS settings. The state
# (tailnets and their Tailscale keys) stays.
uninstall: ## Stop the service and remove what make install installed (keeps the state)
ifeq ($(OS),Darwin)
	[ -n "$(DESTDIR)" ] || launchctl bootout system/$(LAUNCHD_LABEL) 2>/dev/null || true
	rm -f $(LAUNCHD_PLIST)
	@echo "State kept in /Library/Application Support/tailweave"
else
	[ -n "$(DESTDIR)" ] || systemctl disable --now tailweave 2>/dev/null || true
	rm -f $(SYSTEMD_UNIT)
	[ -n "$(DESTDIR)" ] || systemctl daemon-reload 2>/dev/null || true
	@echo "State kept in /var/lib/tailweave"
endif
	rm -f $(BINDIR)/tailweave

clean: ## Remove build output
	rm -rf tailweave bin

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

# --- macOS VM --------------------------------------------------------------

# The macOS installer needs a display; once installed, the VM runs headless.
mac-vm-up: ## Create/start the macOS VM and connect it to the lab in the Linux VM
	@if limactl list -q | grep -qx $(MAC_VM); then limactl start $(MAC_VM); else \
		limactl start --tty=false --name $(MAC_VM) \
			--set '.mounts=[{"location":"$(CURDIR)","writable":true}]' lab/lima/tailweave-mac.yaml && \
		limactl stop $(MAC_VM) && \
		limactl edit --tty=false $(MAC_VM) --set '.video.display="none"' && \
		limactl start $(MAC_VM); fi
	lab/scripts/mac-setup.sh $(VM) $(MAC_VM)

mac-vm-down: ## Stop the macOS VM
	limactl stop $(MAC_VM)

mac-vm-destroy: ## Delete the macOS VM
	limactl delete --force $(MAC_VM)

mac-sh: ## Shell inside the macOS VM, in the repo directory
	$(MAC)

# tailscale and tailscaled (the version in go.mod) are for TestForeignTailscale. go install
# builds them outside this module, so their CLI's dependencies stay out of go.mod.
mac-build: ## Cross-build tailweave, the lab tests and tailscale(d) for macOS into ./bin/darwin
	$(SH) '$(DARWIN_GO) build -o bin/darwin/ ./cmd/... && \
		$(DARWIN_GO) test -c -tags lab -o bin/darwin/daemon.test ./internal/daemon && \
		v=$$(go list -m -f "{{.Version}}" tailscale.com) && \
		GOBIN= $(DARWIN_GO) install tailscale.com/cmd/tailscaled@$$v tailscale.com/cmd/tailscale@$$v && \
		cp "$$(go env GOPATH)"/bin/darwin_arm64/tailscale "$$(go env GOPATH)"/bin/darwin_arm64/tailscaled bin/darwin/'

mac-run: mac-build ## Run the tailweave daemon in the macOS VM (then make mac-login)
	$(MAC) sudo bin/darwin/tailweave run -config lab/tailweave.json

mac-login: mac-build ## Log `make mac-run` into both lab tailnets with their preauth keys
	lab/scripts/mac-login.sh $(VM) $(MAC_VM)

mac-test-lab: mac-build ## Integration tests in the macOS VM against the lab (make lab-up mac-vm-up first)
	lab/scripts/mac-setup.sh $(VM) $(MAC_VM)
	limactl shell --workdir $(CURDIR)/internal/daemon $(MAC_VM) \
		env TAILWEAVE_BIN=$(CURDIR)/bin/darwin/tailweave $(CURDIR)/bin/darwin/daemon.test -test.count=1 $(MAC_TEST_FLAGS)

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

run: lab-build ## Run the tailweave daemon (then make lab-login; control it with sudo bin/tailweave ...)
	$(IN_LAB) sudo bin/tailweave run -config lab/tailweave.json

lab-login: lab-build ## Log `make run` into both lab tailnets with their preauth keys
	$(LAB)/login.sh

ui: lab-build ## Web UI for `make run` on 127.0.0.1:5250 in the VM
	$(IN_LAB) sudo bin/tailweave ui

ui-check: ## Click through the UI in headless Chromium, screenshots in lab/.state/ui
	$(SH) 'lab/scripts/ui-check.sh'

ui-tunnel: ## Make the UI of `make ui` reachable at http://127.0.0.1:5250 on the host
	ssh -F $(HOME)/.lima/$(VM)/ssh.config -N -L 5250:127.0.0.1:5250 lima-$(VM)

# --- Go --------------------------------------------------------------------

lab-build: ## Build into ./bin in the VM, for the lab (VERSION sets what `tailweave version` prints)
	$(SH) 'go build -ldflags "-X main.version=$(VERSION)" -o bin/ ./cmd/...'

test: ## Unit tests
	$(SH) 'go test $(GO_TEST_FLAGS) ./...'

test-lab: ## Integration tests against the running lab (make lab-up first), both backends
	$(SH) 'TAILWEAVE_BACKEND=netns go test -tags lab -count=1 -p 1 $(GO_TEST_FLAGS) ./...'
	$(SH) 'TAILWEAVE_BACKEND=tsnet go test -tags lab -count=1 -p 1 $(GO_TEST_FLAGS) ./internal/daemon'

lint: ## go vet + golangci-lint (if installed)
	$(SH) 'go vet ./... && (command -v golangci-lint >/dev/null && golangci-lint run || true)'

fmt: ## gofmt
	$(SH) 'gofmt -w $$(git ls-files "*.go")'
