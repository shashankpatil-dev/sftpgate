COMPOSE := docker compose -f deploy/compose/docker-compose.yml
LINT    := $(shell go env GOPATH)/bin/golangci-lint
# Kubernetes tools: from PATH, or from .dev/bin if you keep them there.
export PATH := $(CURDIR)/.dev/bin:$(PATH)
CHART   := deploy/helm/sftpgate
SCHEMAS := -schema-location default -schema-location 'https://raw.githubusercontent.com/datreeio/CRDs-catalog/main/{{.Group}}/{{.ResourceKind}}_{{.ResourceAPIVersion}}.json'
ALL_OPTIONS := --set gitSync.enabled=true --set gitSync.repo=https://example.com/config.git --set replicas=3 \
	--set metrics.serviceMonitor.enabled=true --set metrics.prometheusRule.enabled=true \
	--set metrics.grafanaDashboard.enabled=true --set ingress.enabled=true --set podDisruptionBudget.enabled=true \
	--set autoscaling.enabled=true --set priorityClassName=high

PROMETHEUS_IMAGE := prom/prometheus:v3.6.0

.PHONY: build test lint validate check-monitoring dev-keys up up-gitops down logs e2e manifests check-k8s k8s-smoke

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
	bin/sftpgate validate -offline examples/sftpgate.full.yaml
	bin/sftpgate validate -offline deploy/compose/config/sftpgate.yaml

## check-monitoring: check the Prometheus config and run the alert rule tests
check-monitoring:
	docker run --rm -v "$(CURDIR)":/repo --entrypoint promtool $(PROMETHEUS_IMAGE) \
		check config --syntax-only /repo/deploy/compose/prometheus/prometheus.yml
	docker run --rm -v "$(CURDIR)":/repo -w /repo/test/prometheus --entrypoint promtool $(PROMETHEUS_IMAGE) \
		test rules alerts_test.yml

## manifests: regenerate the plain Kubernetes manifests from the Helm chart
manifests:
	@# Rendered to a temp file first: if helm fails, the old file stays as it was.
	@helm template sftpgate $(CHART) --namespace sftpgate > deploy/kubernetes/.sftpgate.yaml.tmp || { rm -f deploy/kubernetes/.sftpgate.yaml.tmp; exit 1; }
	@{ echo "# Generated from deploy/helm/sftpgate by 'make manifests'. Do not edit by hand."; \
	  echo "# See deploy/kubernetes/README.md before applying."; \
	  cat deploy/kubernetes/.sftpgate.yaml.tmp; } > deploy/kubernetes/sftpgate.yaml
	@rm deploy/kubernetes/.sftpgate.yaml.tmp

## check-k8s: lint the chart, validate it against Kubernetes schemas, and check the manifests are up to date
check-k8s:
	helm lint $(CHART)
	helm lint $(CHART) $(ALL_OPTIONS)
	for v in 1.29.0 1.34.0; do \
	  helm template t $(CHART) | kubeconform -strict -summary -kubernetes-version $$v $(SCHEMAS) || exit 1; \
	  helm template t $(CHART) $(ALL_OPTIONS) | kubeconform -strict -summary -kubernetes-version $$v $(SCHEMAS) || exit 1; \
	done
	kubeconform -strict -summary -kubernetes-version 1.34.0 deploy/kubernetes/sftpgate.yaml
	@$(MAKE) -s manifests && git diff --exit-code deploy/kubernetes/sftpgate.yaml \
	  || (echo "deploy/kubernetes/sftpgate.yaml is out of date: run 'make manifests'" && exit 1)

## k8s-smoke: install the chart on a local kind cluster and check it works
k8s-smoke:
	test/k8s/smoke.sh

## dev-keys: create a host key and a demo user key for local testing
dev-keys:
	mkdir -p .dev/keys
	test -f .dev/keys/ssh_host_ed25519_key || ssh-keygen -q -t ed25519 -N "" -f .dev/keys/ssh_host_ed25519_key
	test -f .dev/keys/demo_user || ssh-keygen -q -t ed25519 -N "" -f .dev/keys/demo_user
	test -f .dev/keys/transfer_key || ssh-keygen -q -t ed25519 -N "" -f .dev/keys/transfer_key
	chmod 600 .dev/keys/demo_user
	# A PGP key for the demo's encrypt and decrypt actions, protected with
	# the dev-only passphrase from docker-compose.yml.
	test -f .dev/keys/pgp/demo.asc || { \
		mkdir -p .dev/keys/pgp .dev/keys/gnupg && chmod 700 .dev/keys/gnupg && \
		gpg --homedir .dev/keys/gnupg --batch --pinentry-mode loopback --passphrase dev-only-pgp-passphrase \
			--quick-gen-key "SFTPgate demo <demo@sftpgate.local>" rsa3072 sign,cert never && \
		gpg --homedir .dev/keys/gnupg --batch --pinentry-mode loopback --passphrase dev-only-pgp-passphrase \
			--quick-add-key "$$(gpg --homedir .dev/keys/gnupg --list-keys --with-colons demo@sftpgate.local | awk -F: '/^fpr/ {print $$10; exit}')" rsa3072 encr never && \
		gpg --homedir .dev/keys/gnupg --armor --export demo@sftpgate.local > .dev/keys/pgp/demo.pub.asc && \
		gpg --homedir .dev/keys/gnupg --batch --pinentry-mode loopback --passphrase dev-only-pgp-passphrase \
			--armor --export-secret-keys demo@sftpgate.local > .dev/keys/pgp/demo.asc; }
	# Dev only: the container runs as a different user and must read these keys.
	chmod 644 .dev/keys/ssh_host_ed25519_key .dev/keys/transfer_key .dev/keys/pgp/demo.asc

## up: start SFTPgate with the S3 mock and the GCS emulator
up: dev-keys
	$(COMPOSE) up -d --build
	

## up-gitops: like up, but the config comes from a git repository (set GITOPS_REPO, and GITOPS_PATH if needed)
up-gitops: dev-keys
	$(COMPOSE) -f deploy/compose/gitops.yml up -d --build

## down: stop the local setup and delete its data
down:
	$(COMPOSE) down -v

## logs: follow the SFTPgate logs
logs:
	$(COMPOSE) logs -f sftpgate

## e2e: run end-to-end checks against the local setup (run `make up` first)
e2e:
	test/e2e/run.sh
