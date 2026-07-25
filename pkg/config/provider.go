package config

import (
	"fmt"
	"strings"
)

type ProviderConfig struct {
	Name     string `json:"name"`
	Endpoint string `json:"endpoint"`
	ApiKey   string `json:"api_key,omitempty"`
	Model    string `json:"model,omitempty"`
}

type ProvidersFile struct {
	ActiveProvider string                    `json:"active_provider"`
	Providers      map[string]ProviderConfig `json:"providers"`
}

func (c *Config) ActivateProvider(name string) error {
	if c == nil {
		return fmt.Errorf("config is nil")
	}
	if strings.TrimSpace(name) == "" {
		c.ActiveProvider = ""
		return nil
	}
	if c.Providers == nil {
		return fmt.Errorf("provider '%s' not found", name)
	}

	provider, ok := c.Providers[name]
	if !ok {
		return fmt.Errorf("provider '%s' not found", name)
	}
	if strings.TrimSpace(provider.Endpoint) == "" {
		return fmt.Errorf("provider '%s' has an empty endpoint", name)
	}

	c.ActiveProvider = name
	c.Endpoint = provider.Endpoint
	c.ApiKey = provider.ApiKey
	if provider.Model != "" {
		c.Model = provider.Model
	}
	return nil
}

func (c *Config) SyncActiveProvider() {
	if c == nil || c.ActiveProvider == "" {
		return
	}
	_ = c.ActivateProvider(c.ActiveProvider)
}

func (c *Config) UpdateActiveProvider() {
	if c.ActiveProvider == "" || c.Providers == nil {
		return
	}
	p, ok := c.Providers[c.ActiveProvider]
	if !ok {
		return
	}
	p.Endpoint = c.Endpoint
	p.ApiKey = c.ApiKey
	p.Model = c.Model
	c.Providers[c.ActiveProvider] = p
}
