# Context archive fidelity — unreleased

`CompactPortable` and `NewArtifact` preserve structured JSON number literals,
including integers outside the safe JavaScript range, precise decimals and
exponent spellings. JSON keys are sorted and whitespace/escaping may be
normalized; this is not a byte-for-byte promise about raw JSON formatting.
Plain text is unchanged. New ambiguous JSON (duplicate or case-fold-equivalent
keys, malformed Unicode) is rejected instead of silently rewritten. The size
ceiling and scoped reader are unchanged.

Archive payloads that are byte-identical under the legacy canonical encoder
retain their ID, digest, token estimate and unversioned JSON shape. Only a
payload that needs the new representation uses `encoding: "lossless-json-v1"`.
The encoding is included in the digest; unknown encodings and mismatches
fail validation. Archive readers verify the canonical stored JSON, not a
rounded substitute. The original history, Tool permissions and provider replay
privacy rules are unchanged.

Upgrade all readers/workers before allowing marked archives to be written. An
old reader cannot authenticate the new encoded artifact. Stores must round-trip
the encoding and canonical payload, without decoding numbers as floats. The
built-in PostgreSQL adapter already uses a text payload; no schema migration is
needed. Do not re-encode old records: a previously rounded value cannot be
recovered from its old archive. Consult the original Host transcript or Tool
record for such evidence. Existing checkpoint hash formats and frozen v1
summary recipes are unchanged; this is an archive fix, not a migration of all
Runtime JSON formats.
