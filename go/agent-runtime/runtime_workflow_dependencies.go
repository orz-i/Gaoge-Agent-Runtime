package agentruntime

import (
	"errors"
	"sort"
	"strings"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

func (c *workflowDefinitionCompiler) freezeAgentDependency(node *model.WorkflowNode) error {
	manifest, err := c.service.repo.GetAgentManifest(c.ctx, c.actor, node.ManifestRef)
	if err != nil {
		return errors.Join(ErrWorkflowDependencyMissing, err)
	}
	if manifest.Status != model.AgentManifestStatusActive {
		return ErrAgentManifestDisabled
	}
	node.ManifestRef = manifest.Ref()
	fingerprint, err := hashWorkflowValue(manifest)
	if err != nil {
		return err
	}
	c.addDependency(model.WorkflowDependency{Kind: model.WorkflowDependencyAgent, Ref: manifest.Ref(), Fingerprint: fingerprint})
	return nil
}

func (c *workflowDefinitionCompiler) freezeWorkflowDependency(node *model.WorkflowNode) (*model.WorkflowDefinition, error) {
	definition, err := c.service.repo.GetWorkflowDefinition(c.ctx, c.actor, node.DefinitionRef)
	if err != nil {
		return nil, errors.Join(ErrWorkflowDependencyMissing, err)
	}
	if definition.Status != model.WorkflowDefinitionStatusActive {
		return nil, ErrWorkflowDefinitionDisabled
	}
	if definition.WorkflowID == c.workflowID {
		return nil, ErrWorkflowDependencyCycle
	}
	if err = c.ensureNoDependencyCycle(definition, map[string]struct{}{c.workflowID: {}}); err != nil {
		return nil, err
	}
	node.DefinitionRef = definition.Ref()
	c.addDependency(model.WorkflowDependency{Kind: model.WorkflowDependencyWorkflow, Ref: definition.Ref(), Fingerprint: definition.DefinitionHash})
	return definition, nil
}

func (c *workflowDefinitionCompiler) ensureNoDependencyCycle(definition *model.WorkflowDefinition, path map[string]struct{}) error {
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
	for _, dependency := range definition.Dependencies {
		if dependency.Kind != model.WorkflowDependencyWorkflow {
			continue
		}
		child, err := c.service.repo.GetWorkflowDefinition(c.ctx, c.actor, dependency.Ref)
		if err != nil {
			return errors.Join(ErrWorkflowDependencyMissing, err)
		}
		if err = c.ensureNoDependencyCycle(child, next); err != nil {
			return err
		}
	}
	return nil
}

func (c *workflowDefinitionCompiler) freezeToolDependency(node *model.WorkflowNode) error {
	if c.service.toolCatalog == nil {
		return ErrWorkflowDependencyMissing
	}
	resolved, unavailable, err := c.service.toolCatalog.ResolveAvailable(c.ctx, c.actor, []string{node.ToolKey}, "", "", "")
	if err != nil {
		return errors.Join(ErrWorkflowDependencyMissing, err)
	}
	if !validResolvedWorkflowTool(node.ToolKey, resolved, unavailable) {
		return errors.Join(ErrWorkflowDependencyMissing, ErrRunToolUnavailable)
	}
	tool := resolved[0]
	if !workflowToolReceiptContractValid(tool) {
		return errors.Join(ErrWorkflowDefinitionInvalid, ErrRunToolProviderReceiptRequired)
	}
	fingerprint, err := hashWorkflowValue(tool)
	if err != nil {
		return err
	}
	c.addDependency(model.WorkflowDependency{
		Kind: model.WorkflowDependencyTool, ToolKey: tool.ToolKey, DefinitionVersion: tool.DefinitionVersion,
		Fingerprint: fingerprint, SideEffectLevel: tool.SideEffectLevel,
	})
	return nil
}

func validResolvedWorkflowTool(toolKey string, resolved []ResolvedTool, unavailable []string) bool {
	if len(unavailable) != 0 || len(resolved) != 1 {
		return false
	}
	return resolved[0].ToolKey == toolKey && strings.TrimSpace(resolved[0].DefinitionVersion) != ""
}

func workflowToolReceiptContractValid(tool ResolvedTool) bool {
	if !toolRequiresProviderReceipt(tool.SideEffectLevel) {
		return true
	}
	return normalizeToolIdempotencyMode(tool.IdempotencyMode) == ToolIdempotencyProviderReceipt
}

func (c *workflowDefinitionCompiler) addDependency(dependency model.WorkflowDependency) {
	key := dependency.Kind + "\x00" + dependency.Ref.ID + "\x00" + dependency.Ref.Revision + "\x00" + dependency.ToolKey + "\x00" + dependency.DefinitionVersion
	c.dependencies[key] = dependency
}

func (c *workflowDefinitionCompiler) sortedDependencies() []model.WorkflowDependency {
	keys := make([]string, 0, len(c.dependencies))
	for key := range c.dependencies {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]model.WorkflowDependency, 0, len(keys))
	for _, key := range keys {
		result = append(result, c.dependencies[key])
	}
	return result
}
