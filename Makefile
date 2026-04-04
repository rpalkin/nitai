.PHONY: init proto vendor up down logs smoke e2e unit

init:
	@if [ ! -f .env ]; then \
		echo "Creating .env from .env.example..."; \
		cp .env.example .env; \
		ENCRYPTION_KEY=$$(python3 -c "import secrets; print(secrets.token_hex(32))"); \
		JWT_SECRET=$$(python3 -c "import secrets; print(secrets.token_hex(32))"); \
		if [ "$$(uname)" = "Darwin" ]; then \
			sed -i '' "s/^ENCRYPTION_KEY=$$/ENCRYPTION_KEY=$$ENCRYPTION_KEY/" .env; \
			sed -i '' "s/^JWT_SECRET=$$/JWT_SECRET=$$JWT_SECRET/" .env; \
		else \
			sed -i "s/^ENCRYPTION_KEY=$$/ENCRYPTION_KEY=$$ENCRYPTION_KEY/" .env; \
			sed -i "s/^JWT_SECRET=$$/JWT_SECRET=$$JWT_SECRET/" .env; \
		fi; \
		echo ".env created with generated ENCRYPTION_KEY and JWT_SECRET"; \
	else \
		echo ".env already exists"; \
	fi

proto:
	buf lint
	buf generate
	cd gen/go && go mod tidy

vendor: proto
	cd lib && go mod tidy
	cd api-server && GOWORK=off go mod vendor
	cd go-services && GOWORK=off go mod vendor
	cd e2e && GOWORK=off go mod vendor

up: init vendor
	docker compose up -d --build

down:
	docker compose down --remove-orphans

logs:
	docker compose logs -f

smoke: init vendor
	@echo "=== Cleaning up stale volumes before smoke test ==="
	docker compose down -v --remove-orphans 2>/dev/null || true
	@PROJECT_NAME=$$(basename "$$(pwd)" | tr '[:upper:]' '[:lower:]' | sed 's/[^a-z0-9_-]/-/g'); \
	docker volume ls -q | grep "^$${PROJECT_NAME}_" | xargs -r docker volume rm 2>/dev/null || true
	@echo "=== Running smoke tests ==="
	./tests/smoke.sh

e2e: init vendor
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
		GOWORK=off go test -v -tags e2e -count=1 -timeout 600s -parallel 4 $$RUN_FLAGS ./... 2>&1 | tee $$E2E_LOG; \
		exit $${PIPESTATUS[0]}

unit: init vendor
	cd go-services && go test ./...
	cd api-server && go test ./...

admin-console-setup:
	cd admin-console && npm install

admin-console-dev:
	cd admin-console && npm run dev

admin-console-build:
	cd admin-console && npm run build
