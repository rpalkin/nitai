.PHONY: proto up down logs smoke e2e unit

proto:
	buf lint
	buf generate
	cd gen/go && go mod tidy

up:
	docker compose up -d --build

down:
	docker compose down --remove-orphans

logs:
	docker compose logs -f

smoke:
	./tests/smoke.sh

e2e:
	docker compose -p e2e build
	docker volume rm e2e_restate-e2e-data 2>/dev/null || true
	@E2E_LOG=$$(mktemp /tmp/e2e-test-XXXXXX) && echo "e2e log: $$E2E_LOG" && \
		cd e2e && \
		DOCKER_HOST=$${DOCKER_HOST:-$$(docker context inspect --format '{{.Endpoints.docker.Host}}')} \
		TESTCONTAINERS_RYUK_DISABLED=true \
		GOWORK=off go test -v -tags e2e -count=1 -timeout 300s ./... 2>&1 | tee $$E2E_LOG; \
		exit $${PIPESTATUS[0]}

unit:
	cd go-services && go test ./...
	cd api-server && go test ./...
