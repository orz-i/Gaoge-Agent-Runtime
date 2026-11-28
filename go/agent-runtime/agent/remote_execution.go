package agent

// SupportsRemoteGoalOnly reports whether an Agent StartRequest asks for only
// protocol-neutral execution identity and a Goal. An A2A Task or ACP Prompt
// cannot prove the locally selected model, tools, approvals, output contract,
// or budgets merely because those fields were present in a Host request.
//
// This check runs both at the Handoff boundary and in every first-party remote
// runner's direct StartRun path, before any durable shadow or remote effect.
func SupportsRemoteGoalOnly(request StartRequest) bool {
	return request.Model == "" && len(request.ModelOptions) == 0 &&
		request.Instructions == "" && request.OutputContract == nil &&
		request.MaxModelOutputTokens == 0 && request.Limits == (Limits{}) &&
		len(request.ToolKeys) == 0 && len(request.InitialToolKeys) == 0 &&
		len(request.RequiredToolKeys) == 0 && len(request.HostedToolGrants) == 0 &&
		request.WorkingStateSummary == nil && request.CallAllowance == nil &&
		request.AutoAllowance == nil && request.ParentRunID == "" &&
		request.OriginFingerprint == "" && request.LogicalParentRunID == "" && request.DeadlineAt == nil
}
