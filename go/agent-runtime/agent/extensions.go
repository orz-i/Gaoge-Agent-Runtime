package agent

const (
	EventRunStarted          = "run.started"
	EventRunWaitingInput     = "run.waiting_input"
	EventRunPausedBudget     = "run.paused_budget"
	EventRunResumed          = "run.resumed"
	EventRunCompleted        = "run.completed"
	EventRunFailed           = "run.failed"
	EventModelStarted        = "model.started"
	EventModelDelta          = "model.delta"
	EventModelCompleted      = "model.completed"
	EventToolRequested       = "tool.requested"
	EventToolStarted         = "tool.started"
	EventToolPending         = "tool.pending"
	EventToolCompleted       = "tool.completed"
	EventInteractionRequired = "interaction.required"
)
