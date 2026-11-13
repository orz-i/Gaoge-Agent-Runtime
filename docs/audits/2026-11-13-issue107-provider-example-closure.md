# Provider and task-agent example bounded closure

Source `cf786cffc2cf1cce1664f8faab9e3d797fc4e40a` passed both compilation commands, full Provider module smoke, and task-agent example package smoke. All five logs including final-state exited zero. This is not the final common-SHA matrix/race/lint acceptance.

Provider production error classification did not change. A sent malformed/duplicate/503/SSE response without authoritative outcome remains Pending/Unknown, including after actual Store-clock expiry and fresh owner recovery. Original source/request/catalog/binding, earlier receipts and budgets, complete snapshot/grant, HTTP counts and no Tool replay are asserted. Two additional legitimate unsent binding cases use rejection before the actual dispatch-intent CAS commits, not a fabricated provider no-effect response. Original profile mismatch and same-original-invocation recovery remain covered.

The shared test helper compares the entire canonical cloned zero Response, preserving all fields. The first smoke exposed nil-versus-empty-list representation, not a lost receipt. All original diagnostic failures are retained in issue107-ea1-provider-example-initial.

The example uses the existing first-party BoundedContextAdapter for the entire Run/Prepare lifecycle. The original real tool.completed receipt commit followed by ACK loss remains unchanged. Lease exclusion is measured before that original callback returns, with independent Acquire returning exact Busy and full state/authority unchanged. Known-return continuation then uses a fresh Runner, the original receipt, fourth Model call and original oracle without Tool replay. An ordinary opaque middleware counterpart remains Unknown even after expiry. The strict interruption classifier is not relaxed.

Oracle v2 and in-process-known-receipt-continuation explicitly replace the old post-return TTL observation, not the task cases. Dataset hashes include the measurement Profile, so all three versioned expected hashes were updated from actual manifests; old-version negative comparisons remain. The original 40-case/160-Model/120-Tool, cancellation and privacy assertions remain. The first failure in the two manifest tests is preserved rather than hidden.

No ledger row changed. H-FROZEN/WP-GAOGE and remote authorization limitations remain. No push, PR or merge. Final complete verification is still required.
