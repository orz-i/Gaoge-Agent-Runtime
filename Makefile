.PHONY: bootstrap metadata fmt-check tidy-check go-test go-race go-vet go-lint go-coverage go-vuln ts-lint ts-typecheck ts-test ts-coverage ts-build unit coverage security eval eval-smoke benchmark benchmark-baseline benchmark-smoke benchmark-compare benchmark-integration benchmark-integration-smoke benchmark-integration-baseline benchmark-integration-compare benchmark-integration-test otel-e2e-smoke integration integration-test integration-otel integration-otel-test a2a-product-check a2a-tck release-check check beta

bootstrap:
	pnpm install --frozen-lockfile

metadata:
	node --test scripts/beta-release-policy.test.mjs scripts/check-pr-body-format.test.mjs
	node scripts/check-beta.mjs

fmt-check:
	node scripts/go-workspace.mjs fmt-check

tidy-check:
	node scripts/go-workspace.mjs tidy-check

go-test:
	node scripts/go-workspace.mjs test

go-race:
	node scripts/go-workspace.mjs race

go-vet:
	node scripts/go-workspace.mjs vet

go-lint:
	node scripts/go-workspace.mjs lint

go-coverage:
	node scripts/go-workspace.mjs coverage

go-vuln:
	node scripts/go-workspace.mjs vuln

ts-lint:
	pnpm run lint

ts-typecheck:
	pnpm run typecheck

ts-test:
	pnpm run test

ts-coverage:
	pnpm run coverage

ts-build:
	pnpm run build

unit: go-test ts-test

coverage: go-coverage ts-coverage

security: go-vuln
	pnpm audit --audit-level high

eval:
	cd go/agent-runtime && go test ./evaluation -run '^TestRuntime.*ScenarioCorpus$$' -count=1 -v
	cd go/agent-runtime-a2a && go test -run '^TestA2ADeterministicScenarioCorpus$$' -count=1 -v
	cd go/agent-runtime-mcp && go test -run '^TestMCPDeterministicScenarioCorpus$$' -count=1 -v

eval-smoke:
	cd go/agent-runtime && go test ./evaluation -run '^TestRuntime.*ScenarioCorpus$$' -count=1
	cd go/agent-runtime-a2a && go test -run '^TestA2ADeterministicScenarioCorpus$$' -count=1
	cd go/agent-runtime-mcp && go test -run '^TestMCPDeterministicScenarioCorpus$$' -count=1

benchmark:
	node scripts/run-benchmarks.mjs --output=coverage/benchmarks/latest.json

benchmark-baseline:
	node scripts/run-benchmarks.mjs --output=benchmarks/baseline.json

benchmark-smoke:
	node scripts/run-benchmarks.mjs --smoke

benchmark-compare: benchmark
	node scripts/compare-benchmarks.mjs --report-only benchmarks/baseline.json coverage/benchmarks/latest.json

benchmark-integration:
	node scripts/run-integration-benchmarks.mjs --output=coverage/benchmarks/integration-latest.json

benchmark-integration-smoke:
	node scripts/run-integration-benchmarks.mjs --smoke

benchmark-integration-baseline:
	node scripts/run-integration-benchmarks.mjs --output=benchmarks/integration-baseline.json

benchmark-integration-compare: benchmark-integration
	node scripts/compare-benchmarks.mjs --report-only benchmarks/integration-baseline.json coverage/benchmarks/integration-latest.json

benchmark-integration-test:
	node scripts/run-integration-benchmarks.mjs --external-services --output=coverage/benchmarks/integration-latest.json

otel-e2e-smoke:
	cd integration/otel-e2e && GOWORK=off go test ./...

integration-test:
	node scripts/run-integration.mjs --external-services

integration:
	node scripts/run-integration.mjs

integration-otel:
	node scripts/run-otel-integration.mjs

integration-otel-test:
	node scripts/run-otel-integration.mjs --external-services

a2a-product-check:
	node scripts/check-a2a-product.mjs

a2a-tck:
	node scripts/run-a2a-tck.mjs

release-check:
	node scripts/check-release.mjs

check: metadata fmt-check tidy-check go-vet go-test go-race go-lint ts-lint ts-typecheck ts-test ts-build eval-smoke benchmark-smoke otel-e2e-smoke a2a-product-check release-check

beta: check coverage security integration integration-otel
