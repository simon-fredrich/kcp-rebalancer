.DEFAULT_GOAL := build

.PHONY: help build test vet deploy-infra teardown-infra deploy-kcp teardown-kcp \
	up down add-shard enable-migrations create-workspaces

help:
	@printf '%s\n' \
		'Available commands:' \
		'  make help                         Show this command overview' \
		'  make build                        Build bin/rebalance' \
		'  make test                         Run Go tests' \
		'  make vet                          Run go vet' \
		'  make deploy-infra                 Create the local kind infrastructure' \
		'  make teardown-infra               Delete the local kind infrastructure' \
		'  make deploy-kcp                   Deploy KCP to the local cluster' \
		'  make teardown-kcp                 Remove KCP from the local cluster' \
		'  make up                           Deploy infrastructure and KCP' \
		'  make down                         Tear down the local infrastructure' \
		'  make add-shard ARGS=<name>         Add a shard' \
		'  make enable-migrations            Enable migration API and feature gate' \
		'  make create-workspaces [ARGS=<count>] Create workspaces (default: 100)'

build:
	go build -o bin/rebalance .

test:
	go test ./...

vet:
	go vet ./...

deploy-infra:
	bash hack/deploy-infra.sh

teardown-infra:
	bash hack/teardown-infra.sh

deploy-kcp:
	bash hack/deploy-kcp.sh

teardown-kcp:
	bash hack/teardown-kcp.sh

up: deploy-infra
	$(MAKE) deploy-kcp

down: teardown-infra

add-shard:
	bash hack/add-shard.sh $(ARGS)

enable-migrations:
	bash hack/enable-migrations.sh

create-workspaces:
	bash hack/create-workspaces.sh $(ARGS)
