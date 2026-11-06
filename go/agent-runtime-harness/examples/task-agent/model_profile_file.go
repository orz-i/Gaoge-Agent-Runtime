package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/jsoncontract"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// This file is part of the example HOST, not the Runtime or either Provider.
// Operators can update a trusted catalog without rebuilding the SDK.
// The baseline's stdin fields are user REQUESTS and never certify effort.
const hostModelProfilesFileEnv = "GAOGE_MODEL_PROFILES_FILE"
const maxHostModelProfilesFileBytes = 256 << 10

var errHostModelProfilesFile = errors.New("invalid or unavailable Host model profile file")

func (config settings) hostModelProfileSource() (model.ModelProfileSource, error) {
	if config.modelProfiles != nil {
		return config.modelProfiles, nil
	}
	if path := os.Getenv(hostModelProfilesFileEnv); path != "" {
		file, err := os.Open(path)
		if err != nil {
			return nil, errHostModelProfilesFile
		}
		defer func() { _ = file.Close() }()
		raw, err := io.ReadAll(io.LimitReader(file, maxHostModelProfilesFileBytes+1))
		if err != nil || len(raw) == 0 || len(raw) > maxHostModelProfilesFileBytes ||
			jsoncontract.ValidateUniqueJSONObject(raw) != nil {
			return nil, errHostModelProfilesFile
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		var profiles model.StaticModelProfiles
		if decoder.Decode(&profiles) != nil || profiles == nil ||
			!errors.Is(decoder.Decode(new(any)), io.EOF) {
			return nil, errHostModelProfilesFile
		}
		return profiles, nil
	}
	if config.allowUnknownStructuredModel {
		// This explicit example Host opt-in permits *trying* a native schema
		// probe for one unknown model. It does not claim support for a user's
		// selected reasoning.effort, forced Tools or omitted-effort sampling.
		return model.StaticModelProfiles{
			config.modelID: {StructuredJSONSchema: true},
		}, nil
	}
	return nil, nil
}
