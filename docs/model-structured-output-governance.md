# Third-round Model Capability and Structured Output governance

Status: opt-in strict JSON Schema output for the existing Agent / Harness model loop. This change does **not** add a Go module, persisted schema version, Kernel state transition, or new Context authority. Text-only callers are unchanged.

## Core contracts

`model.OutputContract{Name, Schema}` is an optional **strict** JSON Schema requirement, not arbitrary text guidance. This deliberately does not add a `strict=false` mode.

- `agent.StartRequest.OutputContract` freezes the contract into Agent Run State and each `ModelInvocation.Request`, which is included in the normal request hash / recovery contract.
- `harness.ConfigSnapshot.OutputContract` is sealed in the immutable Harness Config hash, included in the Context static fingerprint, and forwarded to the Agent. `OpenSession` forwards the sealed configuration without a second transcript/state source.
- `model.CapabilityProvider.Capabilities(ctx, concreteModelID)` is optional for legacy text. For strict output, Agent requires explicit support for the selected **model**, before creating a durable Run. The Provider Router resolves a qualified model ID and delegates this query to the bound provider. No capability provider / unknown model = fail closed.
- Output contracts and JSON document validation reuse `jsoncontract`. This first portable profile accepts object-root strict JSON Schema trees with all object properties required, `additionalProperties:false`, nested objects and arrays, scalar/enum/const values and descriptions. It rejects unknown schema keywords, remote references, optional properties, unions, unsupported constraints, schema nesting above 10 levels, and documents above 64 KiB.
- Provider-enforced format mapping uses OpenAI Responses `text.format={type:"json_schema",name,schema,strict:true}` and Anthropic Messages `output_config.format={type:"json_schema",schema}`, per the official API descriptions. Tool calls remain ordinary Tool calls, not alternate output schemas.
- The Agent validates the terminal JSON **after charging the committed model receipt** and before committing `RunStatusCompleted`. Valid output is persisted as `kernel.Result{ContentType:"application/json",Content:<raw JSON>}`. Legacy text continues to use `ContentType:"text"`. An invalid document gets at most one existing, tool-free completion repair. A second invalid document fails. Explicit provider refusal fails without a repair loop; Anthropic token truncation is distinct from refusal.
- Under breaking ADR 0010, **all** Agent Model Invocations (structured or plain text) freeze their request messages and model-facing Tool schemas before commit. Middleware cannot mutate them or the model/contract/options at dispatch. Use a pre-intent preparer, never a post-intent transformer.

## Example

```go
output := &model.OutputContract{
    Name: "answer",
    Schema: json.RawMessage(`{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}`),
}
client, err := openai.New(openai.Config{APIKey: os.Getenv("OPENAI_API_KEY")})
if err != nil { return err }
runner, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: client})
if err != nil { return err }
result, err := runner.StartRun(ctx, agent.StartRequest{
    ID: "stable-run-id",
    Actor: actor, Thread: thread, Goal: "Extract an answer as JSON",
    Model: "gpt-4.1", OutputContract: output,
})
if err != nil { return err }
// result.Result.ContentType == "application/json"
// result.Result.Content is the validated raw JSON object
```

In a Harness-managed session, put `OutputContract` into `harness.ConfigSnapshot` instead; it is frozen on that Turn.

## Provider/model compatibility policy

Both adapters report structured-output capability for a conservative set of documented model families. They do not claim every present or future model is compatible. Unknown models, private deployments and gateways must be explicitly opted in by the Host using `Config.StructuredOutputModels: []string{...}` after actual verification; the allowlist is not persisted as a runtime-global registry. A provider may still return a 400 when it rejects a schema or a model option; the SDK never degrades to prompt-only JSON, changes `strict`, or silently drops the schema.

Native provider constraints may be narrower than this first common schema profile. Hosts should verify the actual model + tool combination before production deployment. The existing Provider adapters do not implement `TokenAdmissionPlanner`, and strict output adds schema/prompt overhead; Context token estimation and other budget controls remain conservative, not provider-certified.

## Execution and error semantics

| Case | Behavior |
| --- | --- |
| No OutputContract | Legacy text and Tool behavior; capability lookup not required |
| Unknown model / no capability report | Reject before Agent Run creation |
| Malformed or unsupported portable schema | Reject before Agent Run creation |
| Valid terminal schema | Exactly one typed JSON result, frozen output contract and ordinary usage receipt |
| Schema mismatch | One bounded, tool-free repair; then terminal failure |
| Provider refusal | Terminal failed Run; no schema repair or false JSON result |
| Provider reports max-token truncation | Explicit provider error; no partial JSON committed |
| Crash after pending ModelInvocation | Resume sends frozen request, not new schema from Host |
| Terminal Tool-produced result | Must satisfy a frozen output schema or the Agent fails rather than returning a false structured success |

Provider-owned signed thinking / encrypted reasoning `ProviderReplay` remains a private envelope and is never exposed as an OutputContract field or a Context Artifact.

## Not in scope

Image/audio/video content parts, arbitrary native Tools, JSON mode without strict schema, server-side Response Chain ownership, cross-model reasoning transfers, generalized schema negotiation, full Provider token preflight, and schema format coercion. These require separately reviewed evidence.

## Verification

```sh
cd go/agent-runtime
go test -count=1 ./model ./provider/... ./agent ./jsoncontract
cd ../agent-runtime-harness
go test -count=1 ./...
make go-test go-race go-vet go-lint
make eval-smoke
make integration   # Requires a running Docker engine
```

The focused tests cover native unary/SSE Provider request shapes, local function/tool continuation, exact routed-model admission, negative capability/schema profiles, refusal/truncation, Agent repair budget and usage, pending-intent recovery, malformed schema before Run creation, and Harness Session/Context fingerprint changes. Fixture/loopback tests are not a claim that private gateways or official endpoints have been live-validated.
