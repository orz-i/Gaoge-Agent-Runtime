package postgres

import (
	"reflect"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
	"gorm.io/gorm"
)

func TestWorkflowDefinitionHeadQueryBindsIdentityValues(t *testing.T) {
	t.Parallel()

	db := openKernelStoreTestDB(t).Session(&gorm.Session{DryRun: true})
	scope := workflow.DefinitionScope{
		Kind:     workflow.DefinitionScopeTenant,
		TenantID: "tenant' OR 1=1 --",
		ActorID:  "actor'; DROP TABLE workflow_definition_heads; --",
	}
	definitionID := "definition' UNION SELECT * --"
	var record models.WorkflowDefinitionHeadRecord
	query := whereWorkflowDefinitionHead(
		db.Model(&models.WorkflowDefinitionHeadRecord{}), scope, definitionID,
	).Find(&record)
	if query.Error != nil {
		t.Fatalf("build head query: %v", query.Error)
	}
	statement := query.Statement.SQL.String()
	for _, value := range []string{scope.TenantID, scope.ActorID, definitionID} {
		if strings.Contains(statement, value) {
			t.Fatalf("query interpolated identity value %q into SQL %q", value, statement)
		}
	}
	want := []any{string(scope.Kind), scope.TenantID, scope.ActorID, definitionID}
	if !reflect.DeepEqual(query.Statement.Vars, want) {
		t.Fatalf("bound variables = %#v, want %#v", query.Statement.Vars, want)
	}
}
