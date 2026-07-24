package http

import "github.com/gin-gonic/gin"

func (m *Module) RegisterRoutes(auth *gin.RouterGroup) {
	routes := auth.Group("")
	routes.Use(func(c *gin.Context) {
		c.Set(requestIDContextKey, m.Handler.requestID(c))
		c.Next()
	})
	routes.POST("/runs", m.Handler.StartTextRun)
	routes.GET("/runs", m.Handler.ListRuns)
	routes.GET("/runs/:run_id", m.Handler.GetTextRun)
	routes.POST("/runs/:run_id/cancel", m.Handler.CancelRun)
	routes.POST("/runs/:run_id/resume", m.Handler.ResumeTextRun)
	routes.POST("/runs/:run_id/retire", m.Handler.RetireTextRun)
	routes.POST("/runs/:run_id/handoffs", m.Handler.DelegateTextRun)
	routes.GET("/runs/:run_id/task-tree", m.Handler.GetRunTaskTree)
	routes.GET("/runs/:run_id/events", m.Handler.StreamRunEvents)
	routes.GET("/runs/:run_id/events/history", m.Handler.GetRunEventHistory)
	routes.GET("/runs/:run_id/events/:event_id", m.Handler.GetRunEvent)
	routes.GET("/runs/:run_id/plan", m.Handler.GetPlan)
	routes.GET("/runs/:run_id/interactions", m.Handler.ListRunInteractions)
	routes.POST("/runs/:run_id/interactions/:interaction_id/resolve", m.Handler.ResolveRunInteraction)
	routes.GET("/runs/:run_id/checkpoints", m.Handler.ListRunCheckpoints)
	routes.GET("/runs/:run_id/outputs", m.Handler.ListOutputs)
	routes.GET("/runs/:run_id/workbench", m.Handler.GetWorkbench)
	routes.GET("/run-queue", m.Handler.ListRunQueue)
	routes.POST("/run-queue", m.Handler.EnqueueRun)
	routes.PATCH("/run-queue/:queue_id", m.Handler.UpdateRunQueue)
	routes.DELETE("/run-queue/:queue_id", m.Handler.CancelRunQueue)
	routes.POST("/run-queue/:queue_id/prioritize", m.Handler.PrioritizeRunQueue)
	routes.POST("/run-queue/:queue_id/interrupt-and-send", m.Handler.InterruptAndSendRun)
	routes.GET("/outputs", m.Handler.ListUserOutputs)
	routes.GET("/outputs/:output_id", m.Handler.GetOutput)
	routes.GET("/outputs/:output_id/versions", m.Handler.ListOutputVersions)
	routes.GET("/outputs/:output_id/versions/:version/preview", m.Handler.GetOutputPreview)
	routes.GET("/outputs/:output_id/versions/:version/download", m.Handler.DownloadOutput)
	routes.POST("/evidence", m.Handler.CreateEvidence)
	routes.GET("/agent-manifests", m.Handler.ListAgentManifests)
	routes.GET("/agent-manifests/:manifest_id", m.Handler.GetAgentManifest)
}

func (m *Module) RegisterAdminRoutes(admin *gin.RouterGroup) {
	routes := admin.Group("/agentruntime")
	routes.Use(func(c *gin.Context) {
		c.Set(requestIDContextKey, m.Handler.requestID(c))
		c.Next()
	})
	routes.GET("/continuations", m.Handler.ListContinuationJobs)
	routes.POST("/continuations/:job_id/requeue", m.Handler.RequeueDeadLetterContinuationJob)
	routes.GET("/agent-manifests", m.Handler.ListAdminAgentManifests)
	routes.POST("/agent-manifests", m.Handler.CreateAgentManifest)
	routes.POST("/agent-manifests/:manifest_id/revisions", m.Handler.ReviseAgentManifest)
}
