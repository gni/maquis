package config

import "testing"

func TestActivateProviderSynchronizesRuntimeFields(t *testing.T) {
	cfg := &Config{
		Endpoint:       "https://old.example",
		ApiKey:         "old-secret",
		Model:          "old-model",
		ActiveProvider: "old",
		Providers: map[string]ProviderConfig{
			"new": {
				Name:     "new",
				Endpoint: "https://new.example",
				ApiKey:   "",
				Model:    "new-model",
			},
		},
	}

	if err := cfg.ActivateProvider("new"); err != nil {
		t.Fatalf("ActivateProvider() error = %v", err)
	}
	if cfg.ActiveProvider != "new" {
		t.Fatalf("ActiveProvider = %q; want new", cfg.ActiveProvider)
	}
	if cfg.Endpoint != "https://new.example" {
		t.Fatalf("Endpoint = %q; want https://new.example", cfg.Endpoint)
	}
	if cfg.ApiKey != "" {
		t.Fatalf("ApiKey = %q; want empty key from selected provider", cfg.ApiKey)
	}
	if cfg.Model != "new-model" {
		t.Fatalf("Model = %q; want new-model", cfg.Model)
	}
}

func TestActivateProviderRejectsInvalidProfile(t *testing.T) {
	cfg := &Config{
		Providers: map[string]ProviderConfig{
			"broken": {Name: "broken"},
		},
	}

	if err := cfg.ActivateProvider("missing"); err == nil {
		t.Fatal("ActivateProvider() accepted an unknown provider")
	}
	if err := cfg.ActivateProvider("broken"); err == nil {
		t.Fatal("ActivateProvider() accepted an empty endpoint")
	}
}
