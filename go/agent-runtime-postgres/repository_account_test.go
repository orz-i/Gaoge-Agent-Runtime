package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime-postgres/models"
	"gorm.io/gorm"
)

const accountEraseTenant = "default"

func TestEraseAccountDataWithoutRuns(t *testing.T) {
	repo := newTestRepository(t)
	db := repo.db
	ctx := context.Background()
	target := models.AgentManifestRevisionRecord{
		ManifestID: "agent-target", Revision: 1, Scope: "actor", TenantID: accountEraseTenant, OwnerActorID: "7", Name: "Target", Status: valueActiveC374515E,
		CreatedByTenantID: accountEraseTenant, CreatedByActorID: "7", RequestID: "request-target", RequestFingerprint: "fingerprint-target",
	}
	other := models.AgentManifestRevisionRecord{
		ManifestID: "agent-other", Revision: 1, Scope: "actor", TenantID: accountEraseTenant, OwnerActorID: "8", Name: "Other", Status: valueActiveC374515E,
		CreatedByTenantID: accountEraseTenant, CreatedByActorID: "8", RequestID: "request-other", RequestFingerprint: "fingerprint-other",
	}
	shared := models.AgentManifestRevisionRecord{
		ManifestID: "agent-shared", Revision: 1, Scope: "tenant", TenantID: accountEraseTenant, Name: "Shared", Status: valueActiveC374515E,
		CreatedByTenantID: accountEraseTenant, CreatedByActorID: "7", RequestID: "request-shared", RequestFingerprint: "fingerprint-shared",
	}
	if err := db.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&shared).Error; err != nil {
		t.Fatal(err)
	}
	targetWorkflow := models.WorkflowDefinitionRevisionRecord{
		WorkflowID: "workflow-target", Revision: 1, SchemaVersion: 1, Scope: "actor",
		TenantID: accountEraseTenant, OwnerActorID: "7", Name: "Target workflow", Status: valueActiveC374515E,
		InputSchemaJSON: `{}`, OutputSchemaJSON: `{}`, RootJSON: `{"id":"root","type":"sequence"}`,
		LimitsJSON: `{}`, DependenciesJSON: `[]`, DependencyHash: "dependency-target", DefinitionHash: "definition-target",
		CreatedByTenantID: accountEraseTenant, CreatedByActorID: "7", RequestID: "workflow-request-target", RequestFingerprint: "workflow-fingerprint-target",
	}
	otherWorkflow := targetWorkflow
	otherWorkflow.WorkflowID, otherWorkflow.OwnerActorID, otherWorkflow.Name = "workflow-other", "8", "Other workflow"
	otherWorkflow.CreatedByActorID, otherWorkflow.RequestID = "8", "workflow-request-other"
	otherWorkflow.RequestFingerprint, otherWorkflow.DependencyHash, otherWorkflow.DefinitionHash = "workflow-fingerprint-other", "dependency-other", "definition-other"
	sharedWorkflow := targetWorkflow
	sharedWorkflow.WorkflowID, sharedWorkflow.Scope, sharedWorkflow.OwnerActorID, sharedWorkflow.Name = "workflow-shared", "tenant", "", "Shared workflow"
	sharedWorkflow.RequestID, sharedWorkflow.RequestFingerprint = "workflow-request-shared", "workflow-fingerprint-shared"
	sharedWorkflow.DependencyHash, sharedWorkflow.DefinitionHash = "dependency-shared", "definition-shared"
	for _, workflow := range []models.WorkflowDefinitionRevisionRecord{targetWorkflow, otherWorkflow, sharedWorkflow} {
		if err := db.Create(&workflow).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, cache := range []models.WorkflowCacheEntryRecord{
		{
			CacheKey: "cache-target", TenantID: accountEraseTenant, ActorID: "7", WorkflowID: targetWorkflow.WorkflowID, WorkflowRevision: "1",
			NodeID: "node-target", DependencyHash: "dependency-target", SchemaHash: "schema-target", ContextHash: "context-target",
			InputHash: "input-target", ValueJSON: `{}`, ContentHash: "content-target", ExpiresAt: time.Now().UTC().Add(time.Hour),
		},
		{
			CacheKey: "cache-other", TenantID: accountEraseTenant, ActorID: "8", WorkflowID: otherWorkflow.WorkflowID, WorkflowRevision: "1",
			NodeID: "node-other", DependencyHash: "dependency-other", SchemaHash: "schema-other", ContextHash: "context-other",
			InputHash: "input-other", ValueJSON: `{}`, ContentHash: "content-other", ExpiresAt: time.Now().UTC().Add(time.Hour),
		},
	} {
		if err := db.Create(&cache).Error; err != nil {
			t.Fatal(err)
		}
	}

	if err := repo.EraseAccountData(ctx, 7); err != nil {
		t.Fatalf("erase account without runs: %v", err)
	}
	assertAccountEraseCount(t, db, &models.AgentManifestRevisionRecord{}, "manifest_id = ?", target.ManifestID, 0)
	assertAccountEraseCount(t, db, &models.AgentManifestRevisionRecord{}, "manifest_id = ?", shared.ManifestID, 1)
	assertAccountEraseCount(t, db, &models.AgentManifestRevisionRecord{}, "created_by_actor_id = ?", "", 1)
	assertAccountEraseCount(t, db, &models.AgentManifestRevisionRecord{}, "created_by_actor_id = ?", "8", 1)
	assertAccountEraseCount(t, db, &models.WorkflowDefinitionRevisionRecord{}, "workflow_id = ?", targetWorkflow.WorkflowID, 0)
	assertAccountEraseCount(t, db, &models.WorkflowDefinitionRevisionRecord{}, "workflow_id = ?", sharedWorkflow.WorkflowID, 1)
	assertAccountEraseCount(t, db, &models.WorkflowDefinitionRevisionRecord{}, "created_by_actor_id = ?", "", 1)
	assertAccountEraseCount(t, db, &models.WorkflowDefinitionRevisionRecord{}, "created_by_actor_id = ?", "8", 1)
	assertAccountEraseCount(t, db, &models.WorkflowCacheEntryRecord{}, "actor_id = ?", "7", 0)
	assertAccountEraseCount(t, db, &models.WorkflowCacheEntryRecord{}, "actor_id = ?", "8", 1)
}

func TestEraseAccountDataDeletesOnlyActorRunGraph(t *testing.T) {
	repo := newTestRepository(t)
	db := repo.db
	ctx := context.Background()
	now := time.Now().UTC()
	targetRun := models.RunRecord{
		RunID: "run-target", TenantID: accountEraseTenant, ActorID: "7", ThreadKind: expiryTestThreadConversation, ThreadID: "thread-target",
		Status: "completed", StartedAt: now,
	}
	otherRun := models.RunRecord{
		RunID: "run-other", TenantID: accountEraseTenant, ActorID: "8", ThreadKind: expiryTestThreadConversation, ThreadID: "thread-other",
		Status: "completed", StartedAt: now,
	}
	if err := db.Create(&targetRun).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&otherRun).Error; err != nil {
		t.Fatal(err)
	}
	for _, event := range []models.EventRecord{
		{RunID: targetRun.RunID, TenantID: accountEraseTenant, ActorID: "7", ThreadKind: targetRun.ThreadKind, ThreadID: targetRun.ThreadID, EventScope: "run", EventID: "event-target", EventType: migrationRunCompleted},
		{RunID: otherRun.RunID, TenantID: accountEraseTenant, ActorID: "8", ThreadKind: otherRun.ThreadKind, ThreadID: otherRun.ThreadID, EventScope: "run", EventID: "event-other", EventType: migrationRunCompleted},
	} {
		if err := db.Create(&event).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, execution := range []models.WorkflowExecutionRecord{
		{
			RunID: targetRun.RunID, WorkflowID: "workflow-target", WorkflowRevision: 1,
			DefinitionHash: "definition-target", DependencyHash: "dependency-target", RootRunID: targetRun.RunID,
			BudgetOwnerRunID: targetRun.RunID, Version: 1, Status: "completed", StateJSON: `{}`, VarsJSON: `{}`,
			WaitsJSON: `[]`, CompensationJSON: `[]`, BudgetJSON: `{}`, EnvironmentSnapshot: `{}`,
			WorkspaceSnapshot: `{}`, ThreadSnapshotHash: "thread-target", StartedAt: now, EndedAt: &now,
		},
		{
			RunID: otherRun.RunID, WorkflowID: "workflow-other", WorkflowRevision: 1,
			DefinitionHash: "definition-other", DependencyHash: "dependency-other", RootRunID: otherRun.RunID,
			BudgetOwnerRunID: otherRun.RunID, Version: 1, Status: "completed", StateJSON: `{}`, VarsJSON: `{}`,
			WaitsJSON: `[]`, CompensationJSON: `[]`, BudgetJSON: `{}`, EnvironmentSnapshot: `{}`,
			WorkspaceSnapshot: `{}`, ThreadSnapshotHash: "thread-other", StartedAt: now, EndedAt: &now,
		},
	} {
		if err := db.Create(&execution).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, result := range []models.RunResultRecord{
		{RunID: targetRun.RunID, RuntimeKind: "workflow", CanonicalJSON: `{"target":true}`, SchemaHash: "schema-target", ContentHash: "content-target"},
		{RunID: otherRun.RunID, RuntimeKind: "workflow", CanonicalJSON: `{"other":true}`, SchemaHash: "schema-other", ContentHash: "content-other"},
	} {
		if err := db.Create(&result).Error; err != nil {
			t.Fatal(err)
		}
	}

	if err := repo.EraseAccountData(ctx, 7); err != nil {
		t.Fatalf("erase account run graph: %v", err)
	}
	assertAccountEraseCount(t, db, &models.RunRecord{}, "actor_id = ?", "7", 0)
	assertAccountEraseCount(t, db, &models.EventRecord{}, "actor_id = ?", "7", 0)
	assertAccountEraseCount(t, db, &models.RunRecord{}, "actor_id = ?", "8", 1)
	assertAccountEraseCount(t, db, &models.EventRecord{}, "actor_id = ?", "8", 1)
	assertAccountEraseCount(t, db, &models.WorkflowExecutionRecord{}, "run_id = ?", targetRun.RunID, 0)
	assertAccountEraseCount(t, db, &models.RunResultRecord{}, "run_id = ?", targetRun.RunID, 0)
	assertAccountEraseCount(t, db, &models.WorkflowExecutionRecord{}, "run_id = ?", otherRun.RunID, 1)
	assertAccountEraseCount(t, db, &models.RunResultRecord{}, "run_id = ?", otherRun.RunID, 1)
}

func assertAccountEraseCount(t *testing.T, db *gorm.DB, model interface{}, query string, value string, want int64) {
	t.Helper()
	var count int64
	if err := db.Model(model).Where(query, value).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("%T count = %d, want %d", model, count, want)
	}
}
