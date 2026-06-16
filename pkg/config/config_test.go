package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestConfigProviders(t *testing.T) {
	// Create a temporary directory for config file
	tmpDir, err := os.MkdirTemp("", "maquis-config-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	configPath := filepath.Join(tmpDir, "config.json")

	// 1. Load default config and make sure it has empty Providers
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	if cfg.Providers == nil {
		t.Fatalf("expected Providers map to be initialized")
	}

	if len(cfg.Providers) != 0 {
		t.Errorf("expected Providers map to be empty, got %d items", len(cfg.Providers))
	}

	// 2. Add some providers
	cfg.Providers["openai"] = ProviderConfig{
		Name:     "openai",
		Endpoint: "https://api.openai.com",
		ApiKey:   "sk-test-key",
		Model:    "gpt-4o",
	}

	cfg.Providers["local"] = ProviderConfig{
		Name:     "local",
		Endpoint: "http://localhost:11434",
		ApiKey:   "",
		Model:    "llama3",
	}

	// 3. Save and reload to verify persistence
	err = SaveConfig(configPath, cfg)
	if err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	cfgReloaded, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("failed to reload config: %v", err)
	}

	if len(cfgReloaded.Providers) != 2 {
		t.Errorf("expected 2 providers after reload, got %d", len(cfgReloaded.Providers))
	}

	pOpenAI, ok := cfgReloaded.Providers["openai"]
	if !ok || pOpenAI.Endpoint != "https://api.openai.com" || pOpenAI.Model != "gpt-4o" {
		t.Errorf("openai provider was not correctly reloaded: %+v", pOpenAI)
	}

	// 4. Test select/sync active provider
	cfgReloaded.ActiveProvider = "openai"
	cfgReloaded.SyncActiveProvider()

	if cfgReloaded.Endpoint != "https://api.openai.com" {
		t.Errorf("expected endpoint to be synced to https://api.openai.com, got %q", cfgReloaded.Endpoint)
	}
	if cfgReloaded.Model != "gpt-4o" {
		t.Errorf("expected model to be synced to gpt-4o, got %q", cfgReloaded.Model)
	}
	if cfgReloaded.ApiKey != "sk-test-key" {
		t.Errorf("expected API Key to be synced to sk-test-key, got %q", cfgReloaded.ApiKey)
	}

	// 5. Test update active provider
	cfgReloaded.Model = "gpt-4-turbo"
	cfgReloaded.UpdateActiveProvider()

	pOpenAIUpdated := cfgReloaded.Providers["openai"]
	if pOpenAIUpdated.Model != "gpt-4-turbo" {
		t.Errorf("expected active provider's model to be updated to gpt-4-turbo, got %q", pOpenAIUpdated.Model)
	}
}

func TestConfigProvidersUnmarshal(t *testing.T) {
	jsonStr := `{
		"endpoint": "http://localhost:8080",
		"model": "llama-3-instruct",
		"providers": {
			"openai": {
				"name": "openai",
				"endpoint": "https://api.openai.com",
				"api_key": "sk-12345",
				"model": "gpt-3.5-turbo"
			}
		},
		"active_provider": "openai"
	}`

	var cfg Config
	err := json.Unmarshal([]byte(jsonStr), &cfg)
	if err != nil {
		t.Fatalf("failed to unmarshal JSON: %v", err)
	}

	if cfg.ActiveProvider != "openai" {
		t.Errorf("expected ActiveProvider to be openai, got %q", cfg.ActiveProvider)
	}

	cfg.SyncActiveProvider()

	if cfg.Endpoint != "https://api.openai.com" {
		t.Errorf("expected endpoint to be synced to provider's endpoint, got %q", cfg.Endpoint)
	}
	if cfg.ApiKey != "sk-12345" {
		t.Errorf("expected API Key to be synced to provider's API Key, got %q", cfg.ApiKey)
	}
	if cfg.Model != "gpt-3.5-turbo" {
		t.Errorf("expected model to be synced to provider's model, got %q", cfg.Model)
	}
}
