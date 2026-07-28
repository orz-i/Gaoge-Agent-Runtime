package agentruntime

import (
	"errors"
	"fmt"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

func (c *workflowDefinitionCompiler) validateNodeCache(node model.WorkflowNode) error {
	if node.Cache == nil || !node.Cache.Enabled {
		return nil
	}
	if node.Cache.TTLSeconds <= 0 || node.Cache.TTLSeconds > c.service.workflowCeilings().MaxCacheTTLSeconds {
		return ErrWorkflowDefinitionInvalid
	}
	validators := map[string]func(model.WorkflowNode) error{
		model.WorkflowNodeAgent:    c.validateAgentNodeCache,
		model.WorkflowNodeTool:     c.validateToolNodeCache,
		model.WorkflowNodeWorkflow: c.validateNestedWorkflowNodeCache,
	}
	validator, ok := validators[node.Type]
	if !ok {
		return ErrWorkflowDefinitionInvalid
	}
	return validator(node)
}

func (c *workflowDefinitionCompiler) validateAgentNodeCache(node model.WorkflowNode) error {
	return invalidWorkflowNodeIf(len(node.OutputSchema) == 0)
}

func (c *workflowDefinitionCompiler) validateToolNodeCache(node model.WorkflowNode) error {
	for _, dependency := range c.dependencies {
		if dependency.Kind == model.WorkflowDependencyTool && dependency.ToolKey == node.ToolKey && dependency.SideEffectLevel == ToolSideEffectRead {
			return nil
		}
	}
	return ErrWorkflowDefinitionInvalid
}

func (c *workflowDefinitionCompiler) validateNestedWorkflowNodeCache(node model.WorkflowNode) error {
	definition, err := c.service.repo.GetWorkflowDefinition(c.ctx, c.actor, node.DefinitionRef)
	if err != nil {
		return ErrWorkflowDefinitionInvalid
	}
	return invalidWorkflowNodeIf(!workflowDefinitionStaticallyPure(*definition))
}

func workflowDefinitionStaticallyPure(definition model.WorkflowDefinition) bool {
	for _, dependency := range definition.Dependencies {
		if dependency.Kind == model.WorkflowDependencyAgent || dependency.Kind == model.WorkflowDependencyTool && dependency.SideEffectLevel != ToolSideEffectRead {
			return false
		}
	}
	return workflowNodeStaticallyNonWaiting(definition.Root)
}

func workflowNodeStaticallyNonWaiting(node model.WorkflowNode) bool {
	switch node.Type {
	case model.WorkflowNodeInteraction, model.WorkflowNodeTimer, model.WorkflowNodeAgent:
		return false
	}
	for _, children := range [][]model.WorkflowNode{node.Children, node.Branches, node.Stages} {
		for _, child := range children {
			if !workflowNodeStaticallyNonWaiting(child) {
				return false
			}
		}
	}
	for _, child := range []*model.WorkflowNode{node.Body, node.Then, node.Else, node.Do, node.Undo} {
		if child != nil && !workflowNodeStaticallyNonWaiting(*child) {
			return false
		}
	}
	return true
}

func workflowUndoNodeAllowed(node model.WorkflowNode) bool {
	switch node.Type {
	case model.WorkflowNodeSequence:
		for _, child := range node.Children {
			if !workflowUndoNodeAllowed(child) {
				return false
			}
		}
		return true
	case model.WorkflowNodeIf:
		return node.Then != nil && workflowUndoNodeAllowed(*node.Then) && (node.Else == nil || workflowUndoNodeAllowed(*node.Else))
	case model.WorkflowNodeSet, model.WorkflowNodeLog, model.WorkflowNodeTool, model.WorkflowNodeWorkflow:
		return true
	default:
		return false
	}
}

func (c *workflowDefinitionCompiler) validateCompensationWorkflow(
	definition *model.WorkflowDefinition,
	path map[string]struct{},
) error {
	if definition == nil {
		return ErrWorkflowDependencyMissing
	}
	if _, exists := path[definition.WorkflowID]; exists {
		return ErrWorkflowDependencyCycle
	}
	next := make(map[string]struct{}, len(path)+1)
	for key := range path {
		next[key] = struct{}{}
	}
	next[definition.WorkflowID] = struct{}{}
	return c.validateCompensationWorkflowRoot(definition.Root, next)
}

func (c *workflowDefinitionCompiler) validateCompensationWorkflowRoot(
	root model.WorkflowNode,
	path map[string]struct{},
) error {
	if root.Type != model.WorkflowNodeSequence || len(root.Children) == 0 {
		return fmt.Errorf("%w: nested compensation workflow must have a sequence root", ErrWorkflowDefinitionInvalid)
	}
	for index := range root.Children {
		allowReturn := index == len(root.Children)-1
		if err := c.validateCompensationWorkflowNode(root.Children[index], path, allowReturn); err != nil {
			return err
		}
	}
	return nil
}

func (c *workflowDefinitionCompiler) validateCompensationWorkflowNode(
	node model.WorkflowNode,
	path map[string]struct{},
	allowReturn bool,
) error {
	switch node.Type {
	case model.WorkflowNodeSequence:
		return c.validateCompensationWorkflowSequence(node.Children, path)
	case model.WorkflowNodeIf:
		return c.validateCompensationWorkflowIf(node, path)
	case model.WorkflowNodeSet, model.WorkflowNodeLog, model.WorkflowNodeTool:
		return nil
	case model.WorkflowNodeWorkflow:
		return c.validateNestedCompensationWorkflow(node.DefinitionRef, path)
	case model.WorkflowNodeReturn:
		if allowReturn {
			return nil
		}
	}
	return fmt.Errorf("%w: unsafe nested compensation workflow node %s", ErrWorkflowDefinitionInvalid, node.Type)
}

func (c *workflowDefinitionCompiler) validateCompensationWorkflowSequence(
	children []model.WorkflowNode,
	path map[string]struct{},
) error {
	for index := range children {
		if err := c.validateCompensationWorkflowNode(children[index], path, false); err != nil {
			return err
		}
	}
	return nil
}

func (c *workflowDefinitionCompiler) validateCompensationWorkflowIf(
	node model.WorkflowNode,
	path map[string]struct{},
) error {
	if node.Then == nil {
		return ErrWorkflowDefinitionInvalid
	}
	if err := c.validateCompensationWorkflowNode(*node.Then, path, false); err != nil {
		return err
	}
	if node.Else == nil {
		return nil
	}
	return c.validateCompensationWorkflowNode(*node.Else, path, false)
}

func (c *workflowDefinitionCompiler) validateNestedCompensationWorkflow(
	ref model.ResourceRef,
	path map[string]struct{},
) error {
	definition, err := c.service.repo.GetWorkflowDefinition(c.ctx, c.actor, ref)
	if err != nil {
		return errors.Join(ErrWorkflowDependencyMissing, err)
	}
	return c.validateCompensationWorkflow(definition, path)
}
