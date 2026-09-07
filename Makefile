# This Makefile is a thin shim over Taskfile.yml.
#
# The cross-cutting Definition of Done requires that the whole platform comes up
# and its tests pass under `make up` with no cloud credentials, so `make` has to
# work. Task is the real runner, because the dependency graph, status checks and
# per-task working directories it provides are things Make expresses badly.
#
# Add new targets to Taskfile.yml, not here.

TASK ?= task

.DEFAULT_GOAL := help

.PHONY: help up down nuke ps logs migrate seed build test test-integration test-all \
        lint fmt tidy generate ci cover psql

help: ## Show available targets
	@$(TASK) --list-all

up: ## Start the local substrate and apply migrations
	@$(TASK) up

down: ## Stop the local substrate, preserving data
	@$(TASK) down

nuke: ## Stop the local substrate and delete all data
	@$(TASK) nuke

ps: ## Show local substrate status
	@$(TASK) ps

logs: ## Tail local substrate logs
	@$(TASK) logs

migrate: ## Apply schema migrations
	@$(TASK) migrate

seed: ## Load local development data
	@$(TASK) seed

build: ## Build all Go packages
	@$(TASK) build

test: ## Run unit tests
	@$(TASK) test

test-integration: ## Run integration tests against the local substrate
	@$(TASK) test:integration

test-all: ## Run unit and integration tests
	@$(TASK) test:all

cover: ## Run tests with coverage
	@$(TASK) cover

lint: ## Run all linters
	@$(TASK) lint

fmt: ## Format sources
	@$(TASK) fmt

tidy: ## Tidy Go modules
	@$(TASK) tidy

generate: ## Generate code from protobuf definitions
	@$(TASK) generate

psql: ## Open a psql shell
	@$(TASK) psql

ci: ## Run everything CI enforces
	@$(TASK) ci
