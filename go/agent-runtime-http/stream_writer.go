package http

import (
	"encoding/json"
	"net/http"
	"sync/atomic"

	"github.com/gin-gonic/gin"
)

func beginNDJSONStream(c *gin.Context) {
	c.Header("Content-Type", "application/x-ndjson; charset=utf-8")
	c.Header("Cache-Control", "no-cache, no-transform")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
}

type streamEventWriter struct {
	c                  *gin.Context
	clientDisconnected atomic.Bool
	publish            func(map[string]interface{}) map[string]interface{}
}

func newStreamEventWriter(c *gin.Context, publish func(map[string]interface{}) map[string]interface{}) *streamEventWriter {
	return &streamEventWriter{
		c:       c,
		publish: publish,
	}
}

func (w *streamEventWriter) Write(payload map[string]interface{}) error {
	if w.publish != nil {
		payload = w.publish(payload)
	}
	if w.clientDisconnected.Load() {
		return nil
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err = w.c.Writer.Write(append(encoded, '\n')); err != nil {
		w.clientDisconnected.Store(true)
		return err
	}
	w.c.Writer.Flush()
	return nil
}
