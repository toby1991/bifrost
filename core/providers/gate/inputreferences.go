package gate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
)

// ExtraParamInputReferences carries []GateInputReference in the existing
// VideoGenerationParameters.ExtraParams. Future video adapters must explicitly
// map and validate their media roles; the Core dispatcher does not infer them.
const ExtraParamInputReferences = "input_references"

func parseInputReferences(value any) ([]GateInputReference, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("invalid input_references: %w", err)
	}
	if len(data) == 0 || data[0] != '[' {
		return nil, fmt.Errorf("input_references must be an array")
	}
	var references []GateInputReference
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&references); err != nil {
		return nil, fmt.Errorf("invalid input_references: %w", err)
	}
	return references, nil
}

func validateInputReferences(references []GateInputReference) error {
	seenFrames := make(map[string]bool, 2)
	for i, reference := range references {
		validRole := false
		switch reference.Type {
		case "image":
			validRole = reference.Role == "first_frame" || reference.Role == "last_frame" || reference.Role == "reference"
		case "video":
			validRole = reference.Role == "reference_video"
		case "audio":
			validRole = reference.Role == "reference_audio"
		}
		if !validRole {
			return fmt.Errorf("input_references[%d] has unsupported type/role combination %q/%q", i, reference.Type, reference.Role)
		}
		if err := validateDownloadLocation(reference.URL); err != nil {
			return fmt.Errorf("input_references[%d] url must be an absolute HTTPS URL without userinfo: %w", i, err)
		}
		parsed, _ := url.Parse(reference.URL) // 上面的校验已保证语法有效。
		if parsed.Hostname() == "" {
			return fmt.Errorf("input_references[%d] url must contain a hostname", i)
		}
		if reference.Role == "first_frame" || reference.Role == "last_frame" {
			if seenFrames[reference.Role] {
				return fmt.Errorf("input_references contains duplicate %s", reference.Role)
			}
			seenFrames[reference.Role] = true
		}
	}
	return nil
}
