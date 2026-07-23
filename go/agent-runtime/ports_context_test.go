package agentruntime

import (
	"context"
	"reflect"
	"testing"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

type contextPortsStub struct{}

func (contextPortsStub) ResolveThread(context.Context, ResolveThreadRequest) (ThreadSnapshot, error) {
	return ThreadSnapshot{}, nil
}

func (contextPortsStub) LoadThreadPath(context.Context, LoadThreadPathRequest) (ThreadPath, error) {
	return ThreadPath{}, nil
}

func (contextPortsStub) ResolveAttachments(context.Context, ResolveAttachmentsRequest) (ResolveAttachmentsResult, error) {
	return ResolveAttachmentsResult{}, nil
}

func (contextPortsStub) OpenAttachment(context.Context, OpenAttachmentRequest) (AttachmentContent, error) {
	return AttachmentContent{}, nil
}

func (contextPortsStub) BeginTurn(context.Context, BeginTurnRequest) (TurnProjection, error) {
	return TurnProjection{}, nil
}

func (contextPortsStub) CompleteTurn(context.Context, CompleteTurnRequest) (ProjectionWriteResult, error) {
	return ProjectionWriteResult{}, nil
}

func (contextPortsStub) FailTurn(context.Context, FailTurnRequest) (ProjectionWriteResult, error) {
	return ProjectionWriteResult{}, nil
}

func (contextPortsStub) CancelTurn(context.Context, CancelTurnRequest) (ProjectionWriteResult, error) {
	return ProjectionWriteResult{}, nil
}

func (contextPortsStub) Within(ctx context.Context, work func(context.Context) error) error {
	return work(ctx)
}

var (
	_ ThreadContextSource  = contextPortsStub{}
	_ TurnProjectionWriter = contextPortsStub{}
	_ AttachmentResolver   = contextPortsStub{}
	_ UnitOfWork           = contextPortsStub{}
)

func TestContextPortContractsDoNotExposeNumericHostIDs(t *testing.T) {
	types := []reflect.Type{
		reflect.TypeFor[ResolveThreadRequest](),
		reflect.TypeFor[ThreadSnapshot](),
		reflect.TypeFor[ThreadInstruction](),
		reflect.TypeFor[LoadThreadPathRequest](),
		reflect.TypeFor[ThreadPath](),
		reflect.TypeFor[ContextMessage](),
		reflect.TypeFor[ThreadCompaction](),
		reflect.TypeFor[ResolveAttachmentsRequest](),
		reflect.TypeFor[ResolveAttachmentsResult](),
		reflect.TypeFor[ResolvedAttachment](),
		reflect.TypeFor[OpenAttachmentRequest](),
		reflect.TypeFor[AttachmentContent](),
		reflect.TypeFor[BeginTurnRequest](),
		reflect.TypeFor[TurnProjection](),
		reflect.TypeFor[TurnUsage](),
		reflect.TypeFor[CompleteTurnRequest](),
		reflect.TypeFor[FailTurnRequest](),
		reflect.TypeFor[CancelTurnRequest](),
		reflect.TypeFor[ProjectionWriteResult](),
	}

	visited := make(map[reflect.Type]bool)
	for _, typ := range types {
		assertNoUnsignedHostID(t, typ, typ.Name(), visited)
	}
}

func assertNoUnsignedHostID(t *testing.T, typ reflect.Type, path string, visited map[reflect.Type]bool) {
	t.Helper()
	if typ.Kind() == reflect.Slice && typ.Elem().Kind() == reflect.Uint8 {
		return
	}
	for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array {
		typ = typ.Elem()
	}
	if typ.Kind() == reflect.Struct {
		pkg := typ.PkgPath()
		if pkg != reflect.TypeFor[ResolveThreadRequest]().PkgPath() && pkg != reflect.TypeFor[domain.ActorRef]().PkgPath() {
			return
		}
	}
	if visited[typ] {
		return
	}
	visited[typ] = true

	switch typ.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		t.Fatalf("context port contract exposes unsigned host identity at %s (%s)", path, typ)
	case reflect.Struct:
		for index := 0; index < typ.NumField(); index++ {
			field := typ.Field(index)
			assertNoUnsignedHostID(t, field.Type, path+"."+field.Name, visited)
		}
	}
}
