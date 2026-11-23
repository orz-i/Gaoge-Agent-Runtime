-- Breaking, manual pre-stable upgrade. Stop every old writer/worker; reconcile
-- all original pending effects, take a consistent Kernel + Harness backup,
-- and explicitly retire non-Agent Invocations before applying this DDL.
-- Never transform old Application/Team/Workflow work into Agent work.
BEGIN;
DO $$
DECLARE
    invalid_records boolean := false;
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = current_schema()
          AND table_name = 'agent_harness_invocations'
          AND column_name = 'execution_class'
    ) THEN
        EXECUTE 'SELECT EXISTS (SELECT 1 FROM agent_harness_invocations '
             || 'WHERE execution_class IS DISTINCT FROM ''agent'' '
             || 'OR capability_key IS DISTINCT FROM ''runtime.agent'')'
          INTO invalid_records;
    ELSE
        SELECT EXISTS (
            SELECT 1 FROM agent_harness_invocations
            WHERE capability_key IS DISTINCT FROM 'runtime.agent'
        ) INTO invalid_records;
    END IF;
    IF invalid_records THEN
        RAISE EXCEPTION 'obsolete/non-Agent Invocations remain; reconcile and explicitly retire them before the Agent-only cutover';
    END IF;
END
$$;
ALTER TABLE agent_harness_invocations DROP COLUMN IF EXISTS execution_class;
COMMIT;
