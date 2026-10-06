.PHONY: bootstrap metadata fmt-check tidy-check go-test go-race go-vet go-lint go-coverage go-vuln ts-lint ts-typecheck ts-test ts-coverage ts-build unit coverage security integration integration-test a2a-product-check a2a-tck release-check check beta

bootstrap:
	pnpm install --frozen-lockfile

metadata:
	node --test scripts/beta-release-policy.test.mjs
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

integration-test:
	node scripts/run-integration.mjs --external-services

integration:
	node scripts/run-integration.mjs

a2a-product-check:
	node scripts/check-a2a-product.mjs

a2a-tck:
	node scripts/run-a2a-tck.mjs

release-check:
	node scripts/check-release.mjs

check: metadata fmt-check tidy-check go-vet go-test go-race go-lint ts-lint ts-typecheck ts-test ts-build a2a-product-check release-check

beta: check coverage security integration
