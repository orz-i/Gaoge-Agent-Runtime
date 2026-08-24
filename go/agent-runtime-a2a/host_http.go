package a2a

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	a2asdk "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
)

const hostedAgentCardMaxAge = 5 * time.Minute

type hostHistoryLengthContextKey struct{}

type hostResponseInterceptor struct {
	a2asrv.PassthroughCallInterceptor
}

func (hostResponseInterceptor) Before(
	ctx context.Context,
	callContext *a2asrv.CallContext,
	request *a2asrv.Request,
) (context.Context, any, error) {
	if callContext == nil || callContext.Method() != "SendMessage" || request == nil {
		return ctx, nil, nil
	}
	sendRequest, ok := request.Payload.(*a2asdk.SendMessageRequest)
	if !ok || sendRequest == nil || sendRequest.Config == nil || sendRequest.Config.HistoryLength == nil {
		return ctx, nil, nil
	}
	return context.WithValue(ctx, hostHistoryLengthContextKey{}, *sendRequest.Config.HistoryLength), nil, nil
}

func (hostResponseInterceptor) After(
	ctx context.Context,
	_ *a2asrv.CallContext,
	response *a2asrv.Response,
) error {
	historyLength, ok := ctx.Value(hostHistoryLengthContextKey{}).(int)
	if !ok || !successfulHostResponse(response) || historyLength < 0 {
		return nil
	}
	task, ok := response.Payload.(*a2asdk.Task)
	if !ok || task == nil {
		return nil
	}
	cloned := *task
	if historyLength == 0 {
		cloned.History = []*a2asdk.Message{}
	} else if historyLength < len(task.History) {
		cloned.History = append([]*a2asdk.Message(nil), task.History[len(task.History)-historyLength:]...)
	}
	response.Payload = &cloned
	return nil
}

func successfulHostResponse(response *a2asrv.Response) bool {
	return response != nil && response.Err == nil
}

type hostedAgentCardHandler struct {
	next         http.Handler
	etag         string
	lastModified time.Time
}

func newHostedAgentCardHandler(card *a2asdk.AgentCard) (http.Handler, error) {
	encoded, err := json.Marshal(card)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(encoded)
	return hostedAgentCardHandler{
		next: a2asrv.NewStaticAgentCardHandler(card), etag: fmt.Sprintf(`"%x"`, digest),
		lastModified: time.Now().UTC().Truncate(time.Second),
	}, nil
}

func (handler hostedAgentCardHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", int(hostedAgentCardMaxAge.Seconds())))
	writer.Header().Set("ETag", handler.etag)
	writer.Header().Set("Last-Modified", handler.lastModified.Format(http.TimeFormat))
	if hostedCardNotModified(request, handler.etag, handler.lastModified) {
		writer.WriteHeader(http.StatusNotModified)
		return
	}
	handler.next.ServeHTTP(writer, request)
}

func hostedCardNotModified(request *http.Request, etag string, lastModified time.Time) bool {
	if request == nil {
		return false
	}
	if value := strings.TrimSpace(request.Header.Get("If-None-Match")); value != "" {
		for candidate := range strings.SplitSeq(value, ",") {
			if strings.TrimSpace(candidate) == etag || strings.TrimSpace(candidate) == "*" {
				return true
			}
		}
		return false
	}
	if value := strings.TrimSpace(request.Header.Get("If-Modified-Since")); value != "" {
		modifiedSince, err := http.ParseTime(value)
		return err == nil && !lastModified.After(modifiedSince)
	}
	return false
}
