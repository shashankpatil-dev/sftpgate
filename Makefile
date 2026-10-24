COMPOSE := docker compose -f deploy/compose/docker-compose.yml
LINT    := $(shell go env GOPATH)/bin/golangci-lint

PROMETHEUS_IMAGE := prom/prometheus:v3.6.0

.PHONY: build test lint validate check-monitoring dev-keys up down logs e2e

## build: compile the sftpgate binary into ./bin
build:
	CGO_ENABLED=0 go build -o bin/sftpgate ./cmd/sftpgate

## test: run all tests with the race detector
test:
	go test -race ./...

## lint: check formatting and code quality
lint:
	$(LINT) run ./...

## validate: check the example configs the way CI checks a pull request
validate: build
	bin/sftpgate validate -offline examples/sftpgate.yaml
	bin/sftpgate validate -offline deploy/compose/config/sftpgate.yaml

## check-monitoring: check the Prometheus config and run the alert rule tests
check-monitoring:
	docker run --rm -v "$(CURDIR)":/repo --entrypoint promtool $(PROMETHEUS_IMAGE) \
		check config --syntax-only /repo/deploy/compose/prometheus/prometheus.yml
	docker run --rm -v "$(CURDIR)":/repo -w /repo/test/prometheus --entrypoint promtool $(PROMETHEUS_IMAGE) \
		test rules alerts_test.yml

## dev-keys: create a host key and a demo user key for local testing
dev-keys:
	mkdir -p .dev/keys
	test -f .dev/keys/ssh_host_ed25519_key || ssh-keygen -q -t ed25519 -N "" -f .dev/keys/ssh_host_ed25519_key
	test -f .dev/keys/demo_user || ssh-keygen -q -t ed25519 -N "" -f .dev/keys/demo_user
	# Dev only: the container runs as a different user and must read the host key.
	chmod 644 .dev/keys/ssh_host_ed25519_key

## up: start SFTPgate with the S3 mock and the GCS emulator
up: dev-keys
	$(COMPOSE) up -d --build
	

## down: stop the local setup and delete its data
down:
	$(COMPOSE) down -v

## logs: follow the SFTPgate logs
logs:
	$(COMPOSE) logs -f sftpgate

## e2e: run end-to-end checks against the local setup (run `make up` first)
e2e:
	test/e2e/run.sh
