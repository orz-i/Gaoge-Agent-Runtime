package http

import (
	"fmt"
	stdhttp "net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

type errorBody struct {
	Error apiError `json:"error"`
}

type apiError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"requestID"`
}

func writeSuccess(c *gin.Context, value interface{}) { c.JSON(stdhttp.StatusOK, value) }

func writePage(c *gin.Context, total int64, results interface{}) {
	c.JSON(stdhttp.StatusOK, gin.H{"total": total, "results": results})
}

func writeError(c *gin.Context, status int, code, message string) {
	if strings.TrimSpace(code) == "" {
		code = inferErrorCode(status)
	}
	c.JSON(status, errorBody{Error: apiError{Code: code, Message: publicMessage(status, message), RequestID: requestID(c)}})
}

func inferErrorCode(status int) string {
	switch status {
	case stdhttp.StatusBadRequest:
		return "request.invalid"
	case stdhttp.StatusUnauthorized:
		return "auth.unauthorized"
	case stdhttp.StatusForbidden:
		return "auth.forbidden"
	case stdhttp.StatusNotFound:
		return "resource.not_found"
	case stdhttp.StatusConflict:
		return "resource.conflict"
	case stdhttp.StatusUnprocessableEntity:
		return "request.unprocessable"
	case stdhttp.StatusTooManyRequests:
		return "request.rate_limited"
	case stdhttp.StatusServiceUnavailable:
		return "runtime.unavailable"
	default:
		return "runtime.internal"
	}
}

func publicMessage(status int, message string) string {
	if status >= stdhttp.StatusInternalServerError && status != stdhttp.StatusServiceUnavailable {
		return "internal runtime error"
	}
	if strings.TrimSpace(message) == "" {
		return stdhttp.StatusText(status)
	}
	return message
}

func invalidBody(c *gin.Context, err error) {
	writeError(c, stdhttp.StatusBadRequest, "request.invalid_json", fmt.Sprintf("invalid request body: %v", err))
}
