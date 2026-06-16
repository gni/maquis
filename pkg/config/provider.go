package config

type ProviderConfig struct {
	Name     string `json:"name"`
	Endpoint string `json:"endpoint"`
	ApiKey   string `json:"api_key,omitempty"`
	Model    string `json:"model,omitempty"`
}

func (c *Config) SyncActiveProvider() {
	if c.ActiveProvider == "" || c.Providers == nil {
		return
	}
	p, ok := c.Providers[c.ActiveProvider]
	if !ok {
		return
	}
	c.Endpoint = p.Endpoint
	if p.ApiKey != "" {
		c.ApiKey = p.ApiKey
	}
	if p.Model != "" {
		c.Model = p.Model
	}
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
