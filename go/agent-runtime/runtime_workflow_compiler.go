package agentruntime

import (
	"fmt"
	"strings"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

func (c *workflowDefinitionCompiler) compileRoot(root *model.WorkflowNode) error {
	if root == nil || root.Type != model.WorkflowNodeSequence || len(root.Children) == 0 ||
		root.Children[len(root.Children)-1].Type != model.WorkflowNodeReturn {
		return fmt.Errorf("%w: root must be a sequence ending in return", ErrWorkflowDefinitionInvalid)
	}
	if err := c.compileNode(root, workflowCompileScope{availableNodes: make(map[string]struct{})}); err != nil {
		return err
	}
	if c.returnCount != 1 {
		return fmt.Errorf("%w: exactly one return is required", ErrWorkflowDefinitionInvalid)
	}
	return nil
}

func (c *workflowDefinitionCompiler) compileNode(node *model.WorkflowNode, scope workflowCompileScope) error {
	if err := c.compileNodeHeader(node); err != nil {
		return err
	}
	if err := c.compileNodeExpressions(*node, scope); err != nil {
		return err
	}
	if err := c.compileNodeStructure(node, scope); err != nil {
		return err
	}
	if node.Cache != nil {
		return c.validateNodeCache(*node)
	}
	return nil
}

func (c *workflowDefinitionCompiler) compileNodeHeader(node *model.WorkflowNode) error {
	if node == nil || strings.TrimSpace(node.ID) == "" || strings.TrimSpace(node.Type) == "" {
		return ErrWorkflowDefinitionInvalid
	}
	node.ID = strings.TrimSpace(node.ID)
	if _, duplicate := c.nodeIDs[node.ID]; duplicate {
		return fmt.Errorf("%w: duplicate node id %s", ErrWorkflowDefinitionInvalid, node.ID)
	}
	c.nodeIDs[node.ID] = struct{}{}
	c.nodeCount++
	if c.nodeCount > c.maxNodes {
		return ErrWorkflowBudgetExceeded
	}
	return validateWorkflowNodeShape(*node)
}

func (c *workflowDefinitionCompiler) compileNodeStructure(node *model.WorkflowNode, scope workflowCompileScope) error {
	handlers := map[string]workflowNodeCompileHandler{
		model.WorkflowNodeSequence:   (*workflowDefinitionCompiler).compileSequenceNode,
		model.WorkflowNodeAgent:      (*workflowDefinitionCompiler).compileAgentNode,
		model.WorkflowNodeTool:       (*workflowDefinitionCompiler).compileToolNode,
		model.WorkflowNodeWorkflow:   (*workflowDefinitionCompiler).compileNestedWorkflowNode,
		model.WorkflowNodeParallel:   (*workflowDefinitionCompiler).compileParallelNode,
		model.WorkflowNodeForEach:    (*workflowDefinitionCompiler).compileForEachNode,
		model.WorkflowNodePipeline:   (*workflowDefinitionCompiler).compilePipelineNode,
		model.WorkflowNodeIf:         (*workflowDefinitionCompiler).compileIfNode,
		model.WorkflowNodeLoop:       (*workflowDefinitionCompiler).compileLoopNode,
		model.WorkflowNodeCompensate: (*workflowDefinitionCompiler).compileCompensationNode,
		model.WorkflowNodeReturn:     (*workflowDefinitionCompiler).compileReturnNode,
	}
	handler, ok := handlers[node.Type]
	if !ok {
		return nil
	}
	return handler(c, node, scope)
}

func (c *workflowDefinitionCompiler) compileSequenceNode(node *model.WorkflowNode, scope workflowCompileScope) error {
	childScope := cloneWorkflowCompileScope(scope)
	for index := range node.Children {
		if err := c.compileNode(&node.Children[index], childScope); err != nil {
			return err
		}
		childScope.availableNodes[node.Children[index].ID] = struct{}{}
	}
	return nil
}

func (c *workflowDefinitionCompiler) compileAgentNode(node *model.WorkflowNode, _ workflowCompileScope) error {
	return c.freezeAgentDependency(node)
}

func (c *workflowDefinitionCompiler) compileToolNode(node *model.WorkflowNode, _ workflowCompileScope) error {
	return c.freezeToolDependency(node)
}

func (c *workflowDefinitionCompiler) compileNestedWorkflowNode(node *model.WorkflowNode, scope workflowCompileScope) error {
	definition, err := c.freezeWorkflowDependency(node)
	if err != nil {
		return err
	}
	if !scope.undo {
		return nil
	}
	return c.validateCompensationWorkflow(definition, make(map[string]struct{}))
}

func (c *workflowDefinitionCompiler) compileParallelNode(node *model.WorkflowNode, scope workflowCompileScope) error {
	for index := range node.Branches {
		if err := c.compileNode(&node.Branches[index], cloneWorkflowCompileScope(scope)); err != nil {
			return err
		}
	}
	return nil
}

func (c *workflowDefinitionCompiler) compileForEachNode(node *model.WorkflowNode, scope workflowCompileScope) error {
	childScope := cloneWorkflowCompileScope(scope)
	childScope.item = true
	return c.compileNode(node.Body, childScope)
}

func (c *workflowDefinitionCompiler) compilePipelineNode(node *model.WorkflowNode, scope workflowCompileScope) error {
	childScope := cloneWorkflowCompileScope(scope)
	childScope.item = true
	for index := range node.Stages {
		if err := c.compileNode(&node.Stages[index], childScope); err != nil {
			return err
		}
		childScope.availableNodes[node.Stages[index].ID] = struct{}{}
	}
	return nil
}

func (c *workflowDefinitionCompiler) compileIfNode(node *model.WorkflowNode, scope workflowCompileScope) error {
	if err := c.compileNode(node.Then, cloneWorkflowCompileScope(scope)); err != nil {
		return err
	}
	if node.Else == nil {
		return nil
	}
	return c.compileNode(node.Else, cloneWorkflowCompileScope(scope))
}

func (c *workflowDefinitionCompiler) compileLoopNode(node *model.WorkflowNode, scope workflowCompileScope) error {
	if node.MaxIterations <= 0 {
		return ErrWorkflowDefinitionInvalid
	}
	return c.compileNode(node.Body, cloneWorkflowCompileScope(scope))
}

func (c *workflowDefinitionCompiler) compileCompensationNode(node *model.WorkflowNode, scope workflowCompileScope) error {
	if scope.undo {
		return fmt.Errorf("%w: nested compensation", ErrWorkflowDefinitionInvalid)
	}
	if err := c.compileNode(node.Do, cloneWorkflowCompileScope(scope)); err != nil {
		return err
	}
	undoScope := cloneWorkflowCompileScope(scope)
	undoScope.undo, undoScope.errorContext, undoScope.compensation = true, true, true
	if err := c.compileNode(node.Undo, undoScope); err != nil {
		return err
	}
	if !workflowUndoNodeAllowed(*node.Undo) {
		return fmt.Errorf("%w: illegal compensation node", ErrWorkflowDefinitionInvalid)
	}
	return nil
}

func (c *workflowDefinitionCompiler) compileReturnNode(_ *model.WorkflowNode, scope workflowCompileScope) error {
	c.returnCount++
	if scope.undo {
		return fmt.Errorf("%w: return in compensation", ErrWorkflowDefinitionInvalid)
	}
	return nil
}

func cloneWorkflowCompileScope(scope workflowCompileScope) workflowCompileScope {
	result := scope
	result.availableNodes = make(map[string]struct{}, len(scope.availableNodes))
	for key := range scope.availableNodes {
		result.availableNodes[key] = struct{}{}
	}
	return result
}
