# Deterministic original-owner settlement race: FAIL retained

Production baseline: merged `c79d2eea87952bc09d65cee2c4657215873c1262`.
Pure-test source: `da341986a46207691933cb536bf1b1b94dd3d7c3`.
Actual race-enabled count=1 result: exit 1; the original success oracle fails.
The real approval Mark commits before a channel holds its return. The actual
continuation worker consumes the original wakeup and successfully Finishes that
same grant; complete Run snapshot, original live lease/epoch and Tool=0/Model=1
are checked before the original call is released. Its second Finish reports
ExecutionConflict joined with RootOutcomeUnknown at Running revision 8.
No probability loop, timing sleep, new physical dispatch or changed production
code was used. This proves the reachable ownership/acknowledgement race; the
remote failure itself did not expose its exact scheduling window.

The original c79 push CI job 131893078981 failed the pre-existing
TestApprovedChildAutomaticallyContinuesOwningTeamOnce with the same result.
Complete remote run archives remain in the local main-c79d2ee watch directory;
the public run is https://github.com/orz-i/Gaoge-Agent-Runtime/actions/runs/43427690755.
CodeQL, actual OTel, PostgreSQL/Redis, unit/lint/consumers, coverage, Windows and
vulnerability jobs succeeded; race and its aggregate failed. No rerun concealed
that result. A fix must preserve exact completion and ownership evidence; missing
grant or arbitrary Conflict cannot be treated as success.
