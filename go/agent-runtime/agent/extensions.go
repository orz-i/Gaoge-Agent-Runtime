package agent

const (
	EventRunStarted          = "run.started"
	EventRunWaitingInput     = "run.waiting_input"
	EventRunCompleted        = "run.completed"
	EventRunFailed           = "run.failed"
	EventModelStarted        = "model.started"
	EventModelDelta          = "model.delta"
	EventModelCompleted      = "model.completed"
	EventToolRequested       = "tool.requested"
	EventToolStarted         = "tool.started"
	EventToolCompleted       = "tool.completed"
	EventInteractionRequired = "interaction.required"
)
