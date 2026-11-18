# Provider Transport changelog

This module is independently versioned. Root Runtime versions do not release it.

## [0.1.0-beta.1] — unreleased

- Physically extract the 38 provider protocol adapters from
  `go/agent-runtime-provider/transport` at source commit
  `7e6703451d9c808d6820d7f224af808098091f78`.
- Preserve all 103 original Go source and fixture-test files without wire or API
  behavior changes. The new module import path is
  `github.com/orz-i/Gaoge-Agent-Runtime/go/provider-transport` (package `transport`).
- Remove the old package; no alias, forwarding wrapper or duplicate remains.
- Add standalone module, public consumer and resolved dependency-graph gates.
  Production code uses the standard library; tests use neutral test libraries.
- The independent tag namespace is `go/provider-transport/v<version>`; no tag or
  release is created by the extraction. Live provider and Gaoge/Agent Server
  integration qualification are not implied by deterministic fixture tests.
