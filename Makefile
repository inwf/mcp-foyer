# mcp-foyer
#
# Two toolchains, and three steps that have to happen in order: the
# frontend is built, its output is placed where the Go build can embed
# it, and then the binary is built with the tag that turns embedding on.
# That ordering is the whole reason this file exists — everything else
# here is a plain `go` or `pnpm` command, and can be run as one.

SHELL := /bin/sh

BACKEND  := backend
FRONTEND := frontend

# Where the frontend's output has to land for the embed directive to
# find it. Generated; not in version control.
EMBED := $(BACKEND)/internal/webui/dist

BIN := $(BACKEND)/bin/mcp-foyer

# The tag that switches embedding on. A binary built without it works
# and serves the API; it just has no web interface.
WEBUI_TAG := webui

.DEFAULT_GOAL := help
.PHONY: help check check-backend check-frontend build build-backend web dev clean

help: ## List the targets
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) | awk -F':.*?## ' '{printf "  %-16s %s\n", $$1, $$2}'

# ===== verification =====

check: check-backend check-frontend ## Run every check, both sides

check-backend: ## Vet and test the Go side, with the race detector
	cd $(BACKEND) && gofmt -l . | (! grep .)
	cd $(BACKEND) && go vet ./...
	cd $(BACKEND) && go test -race ./...

check-frontend: ## Type-check, lint and test the web side
	cd $(FRONTEND) && pnpm typecheck
	cd $(FRONTEND) && pnpm lint
	cd $(FRONTEND) && pnpm test

# ===== building =====

build: web build-backend ## Build the single binary, web interface included
	@echo "built $(BIN)"

# Building the frontend and putting its output where the Go build can
# reach it. The copy is what makes `embed` possible at all: an embed
# directive cannot reach outside its own module directory, and the two
# projects are siblings.
web: ## Build the web interface into the backend's embed directory
	cd $(FRONTEND) && pnpm install --frozen-lockfile
	cd $(FRONTEND) && pnpm build
	rm -rf $(EMBED)
	mkdir -p $(EMBED)
	cp -R $(FRONTEND)/dist/. $(EMBED)/

build-backend: ## Build the binary from whatever is in the embed directory
	cd $(BACKEND) && go build -tags $(WEBUI_TAG) -o bin/mcp-foyer ./cmd/mcp-foyer

# ===== development =====

dev: ## Print how to run the two halves against each other
	@echo "Two processes:"
	@echo "  cd $(BACKEND)  && go run ./cmd/mcp-foyer serve"
	@echo "  cd $(FRONTEND) && pnpm dev"
	@echo
	@echo "The dev server proxies /api, /ws and /mcp to the backend, and"
	@echo "keeps the page's own origin so the backend's websocket origin"
	@echo "check is exercised rather than bypassed."

clean: ## Remove build output
	rm -rf $(EMBED) $(FRONTEND)/dist $(BACKEND)/bin
