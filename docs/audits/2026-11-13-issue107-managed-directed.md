# Managed recovery bounded verification

Source: `5ea99c39844691fb456d63e3ab9db55a243c8e9a`.

All seven test commands and final-state command exited zero, with no skipped or failed tests. Memory's minimal full managed Store suite ran once before the cross-Store continuation. The continuation ran Core managed tests, native SQLite, real PostgreSQL, Host Memory, Host PostgreSQL, and the real process-crash integration selector each with `-count=3`. The original pending-commit `os.Exit` recovery, immutable RESUME/CANCEL reservation crash cases and opaque unknown counterexample all actually ran three times. No original crash point or oracle was moved.

The preceding production closure and two fixture corrections were independently source-reviewed. Construction-bound Host authority, internal pre-Leave entropy, nil-vs-explicit-null encoding, PG exact aggregate timestamps, Memory ancestor/index fencing, exact managed receipt format, and opaque refusal semantics retain their original constraints. The last fixture delta supplied a legal child Goal and installed the competition wrapper before admission, arming it only after setup and retaining the original scoped Runtime. Both owner/PARK requests reach barriers before controlled transaction ordering. No post-T0 authority rebind is used.

The old initial and fixture failure evidence remains in `issue107-ea1-managed-initial` and `issue107-ea1-managed-fixture`. This directory adds same-source directed success, not a replacement for those records. Six-module compilation previously passed at d3d7836; it is not represented as compilation evidence at this source. This batch did not run full regression, race, lint, Provider/example closure or final common-SHA matrix checks. No ledger row changed. Overall EA-1 remains NO-GO; H-FROZEN/WP-GAOGE and remote gates remain independently blocked.

Only the first-party bounded orchestration may create a managed safepoint. Arbitrary middleware remains opaque. Construction configuration remains trusted; ordinary calls cannot substitute Host authority. Native SQL tests use actual Store-clock expiry, independent peer connections and close/reopen; Memory uses its controlled clock. The integration tests use actual process exit. Host Store namespace backup/restore requires exclusive restoration, not concurrent cloned authorities.

Final disk availability is recorded in final-state.log. No cache was manually deleted. No push, PR or merge occurred.
