package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"
)

func (c *Client) generateBailianWorld(ctx context.Context, route RouteConfig, input GenerateInput) (*GenerateOutput, error) {
	if err := rejectUnsupportedImageMask(input.ImageEditMask, route.Protocol); err != nil {
		return nil, err
	}
	payload, err := buildBailianWorldRequest(route.UpstreamModel, input)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	route.SingleProviderAttempt = true
	requestCtx, cancel := context.WithTimeout(ctx, resolveMediaPollTimeout(route, input.Options))
	defer cancel()
	path := "/api/v2/apps/" + url.PathEscape(route.UpstreamModel) + "/openapi/v1/worlds"
	parsed, body, debug, err := c.doBailianRequest(requestCtx, route, http.MethodPost, path, encoded, false)
	if err != nil {
		return nil, err
	}
	return c.pollBailianWorld(requestCtx, route, path, parsed, body, debug, input.Options)
}

func (c *Client) pollBailianWorld(requestCtx context.Context, route RouteConfig, path string, parsed map[string]interface{}, body []byte, debug *UpstreamDebugSnapshot, inputOptions map[string]interface{}) (*GenerateOutput, error) {
	worldID := getString(asMap(parsed[valueDataE4BFD03E])[bailianWorldID])
	if worldID == "" {
		return nil, attachUpstreamDebug(fmt.Errorf("%w: world creation outcome unknown; missing encryptedWorldId", errBailianResponse), debug)
	}
	options := cloneMap(inputOptions)
	if _, exists := options[mediaPollIntervalOption]; !exists {
		options[mediaPollIntervalOption] = 3000
	}
	for {
		data := asMap(parsed[valueDataE4BFD03E])
		done, err := bailianWorldState(data, worldID)
		if err != nil {
			return nil, bailianWorldFailure(requestCtx, worldID, err, debug)
		}
		if done {
			return bailianReadyWorld(worldID, data, body, debug)
		}
		if err := waitMediaPoll(requestCtx, resolveMediaPollInterval(options)); err != nil {
			return nil, bailianWorldFailure(requestCtx, worldID, err, debug)
		}
		parsed, body, debug, err = c.doBailianRequest(requestCtx, route, http.MethodGet, path+"/build-status?encryptedWorldId="+url.QueryEscape(worldID), nil, false)
		if err != nil {
			return nil, bailianWorldFailure(requestCtx, worldID, err, debug)
		}
	}
}

func bailianWorldFailure(ctx context.Context, id string, err error, debug *UpstreamDebugSnapshot) error {
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	return attachUpstreamDebug(fmt.Errorf("bailian encryptedWorldId=%s; world may still exist: %w", id, err), debug)
}

func buildBailianWorldRequest(model string, input GenerateInput) (map[string]interface{}, error) {
	if model != bailianModelWorldAdventure && model != bailianModelWorldDirecting && model != bailianModelWorldActing {
		return nil, fmt.Errorf("%w: unsupported HappyOyster model", errBailianInput)
	}
	payload := bailianOptionSubset(input.Options, "perspective", "creationModel", "uploadMode", "resolution", "aspectRatio", "refWorldId", "layout", "narrative", "eventStyle", "scriptList", "inputImages", "firstFrameImage")
	payload["async"] = true
	prompt := buildOpenAIImageGenerationPrompt(input.Messages)
	if strings.TrimSpace(prompt) != "" {
		payload["prompt"] = prompt
	}
	if err := bailianWorldFirstFrame(payload, input); err != nil {
		return nil, err
	}
	if err := validateBailianWorldMode(model, payload); err != nil {
		return nil, err
	}
	if err := validateBailianWorldCreationInput(model, payload, prompt); err != nil {
		return nil, err
	}
	return payload, nil
}

func validateBailianWorldMode(model string, payload map[string]interface{}) error {
	if model == bailianModelWorldAdventure {
		if _, exists := payload["perspective"]; !exists {
			payload["perspective"] = "third_person"
		}
		if err := validateBailianEnum(payload, "perspective", "first_person", "third_person"); err != nil {
			return err
		}
		return validateBailianEnum(payload, "creationModel", "simple")
	}
	if _, exists := payload["resolution"]; !exists {
		payload["resolution"] = "720p"
		if model == bailianModelWorldActing {
			payload["resolution"] = "480p"
		}
	}
	if err := validateBailianEnum(payload, "resolution", "480p", "720p"); err != nil {
		return err
	}
	if model == bailianModelWorldActing {
		if err := validateBailianEnum(payload, "aspectRatio", "9:16", "16:9"); err != nil {
			return err
		}
		return validateBailianEnum(payload, "creationModel", "simple")
	}
	return validateBailianDirectingMode(payload)
}

func validateBailianDirectingMode(payload map[string]interface{}) error {
	if err := validateBailianEnum(payload, "creationModel", "simple", bailianWorldScriptList); err != nil {
		return err
	}
	if getString(payload["creationModel"]) == bailianWorldScriptList {
		if len(asMap(payload["scriptList"])) == 0 {
			return fmt.Errorf("%w: scriptList is required", errBailianInput)
		}
		if _, specified := payload["eventStyle"]; specified {
			return fmt.Errorf("%w: scriptlist does not support eventStyle", errBailianInput)
		}
	}
	return validateBailianWorldReferences(payload)
}

func validateBailianWorldReferences(payload map[string]interface{}) error {
	if raw, exists := payload["inputImages"]; exists {
		images, valid := raw.([]interface{})
		if !valid || len(images) > 6 {
			return fmt.Errorf("%w: Directing supports at most 6 inputImages", errBailianInput)
		}
		for _, image := range images {
			if err := validateBailianWorldImage(asMap(image)); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateBailianWorldImage(image map[string]interface{}) error {
	uri, data := getString(image[valueUrl5D7AD561]), getString(image["base64"])
	if (uri == "") == (data == "") || (uri != "" && !bailianMediaURI(uri, false)) || (data != "" && (!strings.HasPrefix(data, "data:image/") || !bailianMediaURI(data, true))) {
		return fmt.Errorf("%w: world image needs exactly one URL or base64 data URI", errBailianInput)
	}
	if len(data) >= 6*1024*1024*4/3 {
		return fmt.Errorf("%w: world image must be smaller than 6 MB", errBailianInput)
	}
	return nil
}

func bailianReadyWorld(worldID string, data map[string]interface{}, body []byte, debug *UpstreamDebugSnapshot) (*GenerateOutput, error) {
	metadata, marshalErr := json.Marshal(data)
	if marshalErr != nil {
		return nil, marshalErr
	}
	return &GenerateOutput{ResponseID: worldID, Text: worldID, RawJSON: string(body), Debug: debug,
		GeneratedAssets: []GeneratedAsset{{Kind: GeneratedAssetKindWorld, ExternalID: worldID, MIMEType: valueApplicationJson71FA0DF4, MetadataJSON: string(metadata)}}}, nil
}

func bailianWorldFirstFrame(payload map[string]interface{}, input GenerateInput) error {
	images, err := bailianImageInputURLs(input)
	if err != nil {
		return err
	}
	if len(images) > 1 {
		return fmt.Errorf("%w: use inputImages for Directing reference images", errBailianInput)
	}
	if len(images) == 1 {
		if _, exists := payload["firstFrameImage"]; exists {
			return fmt.Errorf("%w: first frame supplied twice", errBailianInput)
		}
		key := valueUrl5D7AD561
		if strings.HasPrefix(images[0], "data:") {
			key = "base64"
		}
		payload["firstFrameImage"] = map[string]interface{}{key: images[0]}
	}

	return nil
}

func bailianWorldState(data map[string]interface{}, worldID string) (bool, error) {
	if getString(data[bailianWorldID]) != worldID {
		return false, errBailianResponse
	}
	switch getString(data["status"]) {
	case "ready":
		return true, nil
	case "generating":
		return false, nil
	case miniMaxStatusFailed:
		return false, fmt.Errorf("%w: world build failed", errBailianResponse)
	default:
		return false, fmt.Errorf("%w: unknown world status", errBailianResponse)
	}
}

func validateBailianWorldCreationInput(model string, payload map[string]interface{}, prompt string) error {
	if getString(payload["creationModel"]) != bailianWorldScriptList && (strings.TrimSpace(prompt) == "" || utf8.RuneCountInString(prompt) > 2000) {
		return fmt.Errorf("%w: world prompt must contain 1-2000 characters", errBailianInput)
	}
	if raw, exists := payload["firstFrameImage"]; exists {
		if err := validateBailianWorldImage(asMap(raw)); err != nil {
			return err
		}
	} else if model != bailianModelWorldDirecting || getString(payload["creationModel"]) == bailianWorldScriptList {
		return fmt.Errorf("%w: firstFrameImage is required", errBailianInput)
	}

	return nil
}
