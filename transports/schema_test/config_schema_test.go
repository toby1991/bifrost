package schema_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// 上游已移除独立 schema 测试；仅迁回 Gate 合同及其必要辅助函数。
func getSchemaPath(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("failed to get caller info")
	}
	schemaPath := filepath.Join(filepath.Dir(filename), "..", "config.schema.json")
	if _, err := os.Stat(schemaPath); err != nil {
		t.Fatalf("config.schema.json not found at %s", schemaPath)
	}
	return schemaPath
}

func navigateJSON(data interface{}, keys ...interface{}) (interface{}, bool) {
	current := data
	for _, key := range keys {
		switch k := key.(type) {
		case string:
			m, ok := current.(map[string]interface{})
			if !ok {
				return nil, false
			}
			current, ok = m[k]
			if !ok {
				return nil, false
			}
		case int:
			arr, ok := current.([]interface{})
			if !ok || k >= len(arr) {
				return nil, false
			}
			current = arr[k]
		default:
			return nil, false
		}
	}
	return current, true
}

func loadSchema(t *testing.T) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(getSchemaPath(t))
	if err != nil {
		t.Fatalf("failed to read schema: %v", err)
	}
	var schema map[string]interface{}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatalf("failed to parse schema: %v", err)
	}
	return schema
}

func compileSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	schemaPath := getSchemaPath(t)
	data, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("failed to read schema: %v", err)
	}
	schemaDoc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("failed to parse schema JSON: %v", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("config.schema.json", schemaDoc); err != nil {
		t.Fatalf("failed to add schema resource: %v", err)
	}
	compiled, err := c.Compile("config.schema.json")
	if err != nil {
		t.Fatalf("failed to compile schema: %v", err)
	}
	return compiled
}

func validateConfig(t *testing.T, schema *jsonschema.Schema, configJSON string) error {
	t.Helper()
	var v interface{}
	if err := json.Unmarshal([]byte(configJSON), &v); err != nil {
		t.Fatalf("invalid test JSON: %v", err)
	}
	return schema.Validate(v)
}

func TestSchemaGateProvider(t *testing.T) {
	schema := loadSchema(t)

	if _, found := navigateJSON(schema, "properties", "providers", "properties", "gate"); !found {
		t.Fatal("providers is missing the gate property")
	}

	baseProviderTypes, found := navigateJSON(schema, "$defs", "custom_provider_config", "properties", "base_provider_type", "enum")
	if !found {
		t.Fatal("custom_provider_config.base_provider_type enum is missing")
	}
	providerTypes, ok := baseProviderTypes.([]interface{})
	if !ok {
		t.Fatal("custom_provider_config.base_provider_type enum is not an array")
	}
	gateAllowed := false
	for _, providerType := range providerTypes {
		if providerType == "gate" {
			gateAllowed = true
			break
		}
	}
	if !gateAllowed {
		t.Fatal("custom_provider_config.base_provider_type does not allow gate")
	}

	compiled := compileSchema(t)
	config := `{
		"providers": {
			"gate": {
				"keys": [{"name": "gate-key", "value": "test-secret", "weight": 1, "models": ["bytedance/seedance-2.0"]}],
				"custom_provider_config": {
					"base_provider_type": "gate",
					"allowed_requests": {
						"video_generation": true,
						"video_retrieve": true,
						"video_download": true
					}
				}
			}
		}
	}`
	if err := validateConfig(t, compiled, config); err != nil {
		t.Fatalf("named Gate provider should be valid, got: %v", err)
	}
}

// 两个网络配置分支必须同时保留显式关闭底层 stale connection retry 的合同。
func TestSchemaStaleConnectionRetry(t *testing.T) {
	schema := loadSchema(t)
	for _, name := range []string{"network_config", "network_config_without_base_url"} {
		t.Run(name, func(t *testing.T) {
			property, found := navigateJSON(schema, "$defs", name, "properties", "disable_stale_connection_retry")
			if !found {
				t.Fatal("network config is missing disable_stale_connection_retry")
			}
			config, ok := property.(map[string]interface{})
			if !ok || config["type"] != "boolean" || config["default"] != false {
				t.Fatalf("disable_stale_connection_retry must remain a boolean defaulting to false: %v", property)
			}
		})
	}
}
