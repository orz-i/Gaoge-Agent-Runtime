.PHONY: provider-transport-check bootstrap metadata agent-baseline agent-quality agent-quality-pilot agent-quality-distractor-pilot agent-quality-probe agent-quality-calibrate agent-baseline-test agent-quality-anchor-check benchmark-agent-policy fmt-check tidy-check go-test go-race go-vet go-lint go-coverage go-vuln ts-lint ts-typecheck ts-test ts-coverage ts-build unit coverage security eval eval-smoke benchmark benchmark-baseline benchmark-smoke benchmark-compare benchmark-integration benchmark-integration-smoke benchmark-integration-baseline benchmark-integration-compare benchmark-integration-test otel-e2e-smoke integration-compile integration integration-test integration-otel integration-otel-test a2a-product-check a2a-tck release-check check-static check-execution check-execution-no-race check check-no-race beta

bootstrap:
	pnpm install --frozen-lockfile

metadata:
	node --test scripts/beta-release-policy.test.mjs scripts/check-pr-body-format.test.mjs scripts/release-command.test.mjs scripts/sqlite-release-policy.test.mjs scripts/go-coverage-summary.test.mjs scripts/provider-transport-policy.test.mjs
	node --test scripts/activate-pr-description-check.test.mjs scripts/check-ci-results.test.mjs scripts/setup-sqlite.test.mjs
	node scripts/check-beta.mjs

agent-baseline:
	python3 scripts/run-agent-baseline.py

agent-baseline-test:
	python3 -m unittest discover -s scripts -p 'test_agent_*.py'

# Read-only, content-free evidence manifest. To verify private original reports,
# use scripts/agent_quality_anchors.py --evidence-dir <directory> explicitly.
agent-quality-anchor-check:
	python3 scripts/agent_quality_anchors.py

# Opt-in live quality: 40 independent samples; may incur actual Provider charges.
agent-quality:
	python3 scripts/run-agent-baseline.py quality

# Paid preflight only: strict JSON + bounded synthetic Tool/replay, no task samples.
agent-quality-probe:
	python3 scripts/run-agent-baseline.py probe

# Paid pilot only: preflight plus exactly one fixed evidence-40 task; no full score.
agent-quality-pilot:
	python3 scripts/run-agent-baseline.py pilot

# Paid narrow verification: preflight plus exactly distractor-05, never a 40-case score.
agent-quality-distractor-pilot:
	python3 scripts/run-agent-baseline.py pilot-distractor

# Local calibration only: scripted Model, same real Agent/Tool/Recovery oracle.
agent-quality-calibrate:
	@go run ./go/agent-runtime-harness/examples/task-agent -mode=quality-scripted

# Optional, report-only policy benchmark on two opposing workload shapes.
# Not an ordinary CI gate: timing is meaningful only on a stable runner.
benchmark-agent-policy:
	cd go/agent-runtime && go test ./agent -run '^TestRepeatedToolPolicyMatchesIndependentNestedOracle$$' -count=1
	cd go/agent-runtime && go test ./agent -run '^$$' -bench 'BenchmarkRepeatedToolPolicy(UniqueTranscript|LongTranscript)$$' -benchmem -benchtime=250ms -count=3

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
	node --test scripts/picomatch-security.test.mjs
	pnpm audit --audit-level moderate

eval:
	cd go/agent-runtime && go test ./evaluation -run '^TestRuntimeScenarioCorpus$$' -count=1 -v
	cd go/agent-runtime-a2a && go test -run '^TestA2ADeterministicScenarioCorpus$$' -count=1 -v
	cd go/agent-runtime-mcp && go test -run '^TestMCPDeterministicScenarioCorpus$$' -count=1 -v

eval-smoke:
	cd go/agent-runtime && go test ./evaluation -run '^TestRuntimeScenarioCorpus$$' -count=1
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

# This E2E module uses GOWORK=off and local module replacements. Compile
# it in ordinary CI too, not only after the expensive real-engine suites.
# A removed SDK API must fail before a PostgreSQL/Redis integration run.
integration-compile:
	cd integration/continuation-e2e && GOWORK=off go test ./... -run '^$$' -count=1

integration-test: integration-compile
	node scripts/run-integration.mjs --external-services

integration: integration-compile
	node scripts/run-integration.mjs

integration-otel:
	node scripts/run-otel-integration.mjs

integration-otel-test:
	node scripts/run-otel-integration.mjs --external-services

a2a-product-check:
	node scripts/check-a2a-product.mjs

a2a-tck:
	node scripts/run-a2a-tck.mjs

provider-transport-check:
	node scripts/check-provider-transport.mjs

release-check:
	node scripts/check-release.mjs

# CI runs race tests on an isolated runner, never alongside tidy-check in the
# same checkout (tidy-check temporarily edits module files).
CHECK_STATIC_TARGETS := metadata agent-baseline-test fmt-check tidy-check ts-lint ts-typecheck integration-compile go-vet go-lint
CHECK_EXECUTION_TARGETS := provider-transport-check go-test go-race ts-test ts-build eval-smoke benchmark-smoke otel-e2e-smoke a2a-product-check release-check
CHECK_TARGETS := $(CHECK_STATIC_TARGETS) $(CHECK_EXECUTION_TARGETS)

check-static: $(CHECK_STATIC_TARGETS)

check-execution: $(CHECK_EXECUTION_TARGETS)

check-execution-no-race: $(filter-out go-race,$(CHECK_EXECUTION_TARGETS))

check: $(CHECK_TARGETS)

check-no-race: $(filter-out go-race,$(CHECK_TARGETS))

beta: check coverage security integration integration-otel
