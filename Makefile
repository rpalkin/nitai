.PHONY: proto vendor up down logs smoke e2e unit

proto:
	buf lint
	buf generate
	cd gen/go && go mod tidy

vendor: proto
	cd api-server && GOWORK=off go mod vendor
	cd go-services && GOWORK=off go mod vendor
	cd e2e && GOWORK=off go mod vendor

up: vendor
	docker compose up -d --build

down:
	docker compose down --remove-orphans

logs:
	docker compose logs -f

smoke:
	@echo "=== Cleaning up stale volumes before smoke test ==="
	docker compose down -v --remove-orphans 2>/dev/null || true
	@PROJECT_NAME=$$(basename "$$(pwd)" | tr '[:upper:]' '[:lower:]' | sed 's/[^a-z0-9_-]/-/g'); \
	docker volume ls -q | grep "^$${PROJECT_NAME}_" | xargs -r docker volume rm 2>/dev/null || true
	@echo "=== Running smoke tests ==="
	./tests/smoke.sh

e2e: vendor
	@echo "=== Cleaning all test data for fresh e2e run ==="
	@echo "Removing Docker volumes..."
	docker volume rm e2e_restate-e2e-data 2>/dev/null || true
	docker compose -p e2e down -v 2>/dev/null || true
	@echo "Clearing local data directories..."
	rm -rf ./db/* 2>/dev/null || true
	rm -rf ./pgdata/* 2>/dev/null || true
	rm -rf ./restate-data/* 2>/dev/null || true
	@echo "Building fresh containers..."
	docker compose -p e2e build
	@RUN_FLAGS=""; \
	if [ -n "$(TEST_INCLUDE)" ]; then \
		RUN_FLAGS="-run $(TEST_INCLUDE)"; \
		echo "Running tests matching: $(TEST_INCLUDE)"; \
	fi; \
	E2E_LOG=$$(mktemp /tmp/e2e-test-XXXXXX) && echo "e2e log: $$E2E_LOG" && \
		cd e2e && \
		DOCKER_HOST=$${DOCKER_HOST:-$$(docker context inspect --format '{{.Endpoints.docker.Host}}')} \
		TESTCONTAINERS_RYUK_DISABLED=true \
		GOWORK=off go test -v -tags e2e -count=1 -timeout 900s $$RUN_FLAGS ./... 2>&1 | tee $$E2E_LOG; \
		exit $${PIPESTATUS[0]}

unit:
	cd go-services && go test ./...
	cd api-server && go test ./...
