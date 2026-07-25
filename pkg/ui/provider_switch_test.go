package ui

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"maquis/pkg/agent"
	"maquis/pkg/config"
	"maquis/pkg/db"
)

func TestCommitProviderConfigRefreshesLiveAgent(t *testing.T) {
	original := &config.Config{
		Endpoint:           "https://old.example",
		ApiKey:             "old-secret",
		Model:              "old-model",
		ActiveProvider:     "old",
		ContextWindowLimit: 128000,
		Providers: map[string]config.ProviderConfig{
			"old": {
				Name:     "old",
				Endpoint: "https://old.example",
				ApiKey:   "old-secret",
				Model:    "old-model",
			},
			"new": {
				Name:     "new",
				Endpoint: "https://new.example",
				Model:    "new-model",
			},
		},
	}
	transport := &providerRecordingTransport{}
	a := agent.NewAgent(
		original,
		filepath.Join(t.TempDir(), "config.json"),
		&http.Client{Transport: transport},
	)
	oldProvider := a.LLMProvider
	if provider, ok := oldProvider.(*agent.OpenAICompatibleProvider); ok {
		provider.ThinkingSupportChecked = true
		provider.ThinkingSupported = true
	}

	// This mirrors the interactive menu, which changes only ActiveProvider.
	next := cloneProviderConfig(a.Config)
	next.ActiveProvider = "new"
	if err := commitProviderConfig(a, next); err != nil {
		t.Fatalf("commitProviderConfig() error = %v", err)
	}

	if a.Config == original {
		t.Fatal("provider switch mutated the old live config instead of committing a new state")
	}
	if original.ActiveProvider != "old" || original.Endpoint != "https://old.example" {
		t.Fatalf("provider switch mutated the previous config: %#v", original)
	}
	if a.Config.ActiveProvider != "new" {
		t.Fatalf("ActiveProvider = %q; want new", a.Config.ActiveProvider)
	}
	if a.Config.Endpoint != "https://new.example" {
		t.Fatalf("Endpoint = %q; want https://new.example", a.Config.Endpoint)
	}
	if a.Config.ApiKey != "" {
		t.Fatalf("ApiKey = %q; want selected provider's empty key", a.Config.ApiKey)
	}
	if a.Config.Model != "new-model" {
		t.Fatalf("Model = %q; want new-model", a.Config.Model)
	}
	if a.LLMProvider == oldProvider {
		t.Fatal("live LLM provider was not refreshed")
	}

	liveProvider, ok := a.LLMProvider.(*agent.OpenAICompatibleProvider)
	if !ok {
		t.Fatalf("LLMProvider type = %T; want *OpenAICompatibleProvider", a.LLMProvider)
	}
	if liveProvider.Config != a.Config {
		t.Fatal("live LLM provider is not bound to the committed config")
	}
	if liveProvider.ThinkingSupportChecked || liveProvider.ThinkingSupported {
		t.Fatal("provider capability cache was not reset after switching endpoints")
	}

	reloaded, err := config.LoadConfig(a.ConfigPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if reloaded.ActiveProvider != "new" || reloaded.Endpoint != "https://new.example" || reloaded.Model != "new-model" {
		t.Fatalf("persisted provider state is inconsistent: %#v", reloaded)
	}

	chunks := make(chan agent.StreamChunk, 4)
	message, err := a.StreamChatCompletions(
		context.Background(),
		[]db.Message{{Role: "user", Content: "hello"}},
		nil,
		chunks,
	)
	if err != nil {
		t.Fatalf("StreamChatCompletions() error = %v", err)
	}
	if message == nil || message.Content != "ok" {
		t.Fatalf("StreamChatCompletions() message = %#v; want content ok", message)
	}
	if len(transport.requestURLs) != 2 {
		t.Fatalf("request count = %d; want capability check and completion request", len(transport.requestURLs))
	}
	for _, requestURL := range transport.requestURLs {
		if !strings.HasPrefix(requestURL, "https://new.example/") {
			t.Fatalf("next request used stale provider URL %q", requestURL)
		}
	}
}

func TestCommitProviderConfigResolvesNoneToDefaultProfile(t *testing.T) {
	cfg := &config.Config{
		Endpoint:       "https://custom.example",
		Model:          "custom-model",
		ActiveProvider: "custom",
		Providers: map[string]config.ProviderConfig{
			"default": {
				Name:     "default",
				Endpoint: "https://default.example",
				Model:    "default-model",
			},
			"custom": {
				Name:     "custom",
				Endpoint: "https://custom.example",
				Model:    "custom-model",
			},
		},
	}
	a := agent.NewAgent(cfg, filepath.Join(t.TempDir(), "config.json"), &http.Client{})
	next := cloneProviderConfig(cfg)
	next.ActiveProvider = ""

	if err := commitProviderConfig(a, next); err != nil {
		t.Fatalf("commitProviderConfig() error = %v", err)
	}
	if a.Config.ActiveProvider != "default" {
		t.Fatalf("ActiveProvider = %q; want default", a.Config.ActiveProvider)
	}
	if a.Config.Endpoint != "https://default.example" || a.Config.Model != "default-model" {
		t.Fatalf("default provider was not activated: %#v", a.Config)
	}
}

type providerRecordingTransport struct {
	requestURLs []string
}

func (transport *providerRecordingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.requestURLs = append(transport.requestURLs, request.URL.String())

	status := http.StatusOK
	body := ""
	if request.URL.Path == "/props" {
		status = http.StatusNotFound
	} else {
		body = "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\n" +
			"data: [DONE]\n\n"
	}

	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    request,
	}, nil
}
