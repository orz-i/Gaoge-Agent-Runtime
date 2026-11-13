# EA-1 B05 diagnostic, not acceptance

Fixed targets: WR-R/WC-R P2, WR-W/WC-W P1/P3/P4. No production edits.
Initial source 793fbc202bce6f96fb81bc9b4f29d2841508a902: all four directed backend commands exit 1. Wait P4 passes. Wait P1 fixture uses invalid same-WaitingInput transition. Both Resume unknown child and Wait P3 child expect an ancestor same-status Apply rejection but receive success.
All three Store implementations explicitly fence every self write but only terminal ancestor writes. EA-03 wording says competing control/writes for self plus full ancestor chain. This scope discrepancy remains under limited review and is not waived.

Variant source 0c6fe33c43b5f1bebbafedd4defa6167607c8659 corrects the invalid self transition and tests ancestor terminal Apply. Four directed -race backend commands pass; PostgreSQL count=3 has no skips. Three module lint commands return 0. This does NOT prove rejection of ancestor nonterminal writes and does NOT authorize marking the original cells PASS.

Both raw runs are preserved with exact source, exit codes, sanitized command runner and SHA256 manifests in evidence/issue107-ea1-b05-initial and evidence/issue107-ea1-b05-terminal-variant. CSV remains 36 PASS / 108 UNRUN, older evidence untouched. No push/PR/merge, no Host changes, no production operations. Disk 9.7 GiB; cleanup 0 bytes.
