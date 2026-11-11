package transport

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

func (c *Client) pollBailianTask(ctx context.Context, route RouteConfig, parsed map[string]interface{}, body []byte, debug *UpstreamDebugSnapshot, options map[string]interface{}) (map[string]interface{}, []byte, *UpstreamDebugSnapshot, error) {
	taskID := getString(asMap(parsed["output"])[bailianTaskID])
	if taskID == "" {
		return nil, body, debug, attachUpstreamDebug(fmt.Errorf("%w: creation returned no task_id; outcome unknown", errBailianResponse), debug)
	}
	for {
		task := asMap(parsed["output"])
		if getString(task[bailianTaskID]) != taskID {
			return nil, body, debug, bailianTaskFailure(ctx, taskID, errBailianResponse, debug)
		}
		done, err := resolveBailianTaskState(task, body, debug)
		if err != nil {
			return nil, body, debug, bailianTaskFailure(ctx, taskID, err, debug)
		}
		if done {
			return parsed, body, debug, nil
		}
		if err = waitMediaPoll(ctx, resolveMediaPollInterval(options)); err != nil {
			return nil, body, debug, bailianTaskFailure(ctx, taskID, err, debug)
		}
		parsed, body, debug, err = c.doBailianRequest(ctx, route, http.MethodGet, bailianNativeBase+"/tasks/"+url.PathEscape(taskID), nil, false)
		if err != nil {
			return nil, body, debug, bailianTaskFailure(ctx, taskID, err, debug)
		}
	}
}

func resolveBailianTaskState(task map[string]interface{}, body []byte, debug *UpstreamDebugSnapshot) (bool, error) {
	switch getString(task[bailianTaskStatus]) {
	case "PENDING", "RUNNING":
		return false, nil
	case "SUCCEEDED":
		return true, nil
	case "FAILED", "CANCELED":
		return true, &UpstreamError{StatusCode: bailianErrorStatus(getString(task["code"])), Message: "Bailian task " + getString(task[bailianTaskStatus]) + ": " + getString(task["code"]) + " " + getString(task["message"]), Body: string(body), Debug: debug}
	default:
		return true, fmt.Errorf("%w: unknown task status %q", errBailianResponse, getString(task[bailianTaskStatus]))
	}
}

func bailianTaskFailure(ctx context.Context, id string, err error, debug *UpstreamDebugSnapshot) error {
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	return attachUpstreamDebug(fmt.Errorf("bailian task_id=%s; query failed, task may still exist: %w", id, err), debug)
}
