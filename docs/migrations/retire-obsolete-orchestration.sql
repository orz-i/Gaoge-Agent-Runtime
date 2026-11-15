-- BREAKING CHANGE: destructive retirement of obsolete first-party Workflow
-- definition storage. Run ONLY after quiescing old workers, external-effect
-- reconciliation, full backup and explicit operator approval. The SDK's
-- ordinary AutoMigrate does not execute these drops.
BEGIN;
DROP TABLE IF EXISTS agent_workflow_definition_heads;
DROP TABLE IF EXISTS agent_workflow_definition_revisions;
COMMIT;
