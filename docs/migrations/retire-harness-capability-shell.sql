-- Explicit pre-stable hard cut. This is NOT a replay/conversion script.
-- Stop ALL old Harness/Runtime writers, settle or explicitly retire unknown
-- physical Provider/Tool effects, and back up Kernel + Harness evidence.
-- The new Agent-only Start admission hash differs from the old Capability
-- envelope; no old Turn/Invocation (even a terminal one eligible for Retry)
-- may be implicitly resumed by the new executor. Explicitly drain/retire
-- them, including their complete physical/Host receipts, before running DDL.
-- Do not edit a historical invocation's capability_key into runtime.agent.
BEGIN;
LOCK TABLE agent_harness_invocations, agent_harness_turns,
    agent_harness_items, agent_harness_budgets,
    agent_harness_managed_recovery IN ACCESS EXCLUSIVE MODE;
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM agent_harness_invocations
        WHERE capability_key IS DISTINCT FROM 'runtime.agent'
    ) THEN
        RAISE EXCEPTION 'obsolete non-Agent Invocation exists; never promote it to first-party Agent';
    END IF;
    IF EXISTS (
        SELECT 1 FROM agent_harness_invocations
        WHERE definition_version IS DISTINCT FROM 'v1'
    ) THEN
        RAISE EXCEPTION 'unknown original Invocation definition version; reconcile and retire it';
    END IF;
    IF EXISTS (SELECT 1 FROM agent_harness_invocations) OR
       EXISTS (SELECT 1 FROM agent_harness_turns) OR
       EXISTS (SELECT 1 FROM agent_harness_items) OR
       EXISTS (SELECT 1 FROM agent_harness_budgets) OR
       EXISTS (SELECT 1 FROM agent_harness_managed_recovery) THEN
        RAISE EXCEPTION 'unretired Turn/Invocation/Item/Budget/Recovery evidence blocks Agent-only identity cutover';
    END IF;
END
$$;
ALTER TABLE agent_harness_invocations
    DROP COLUMN capability_key,
    DROP COLUMN definition_version;
COMMIT;
