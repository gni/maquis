package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"maquis/pkg/agent/tool"
	"maquis/pkg/config"
)

type mcpClient struct {
	name        string
	config      config.MCPServerConfig
	isSSE       bool
	postURL     string
	sseBody     io.ReadCloser
	requests    map[int64]chan string
	requestMu   sync.Mutex
	nextID      int64
	initialized bool
	client      *http.Client
	cancel      context.CancelFunc
}

type MCPTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema"`
}

type mcpToolExecutor struct {
	client   *mcpClient
	toolName string
	def      Tool
}

func (m *mcpToolExecutor) Name() string { return m.def.Function.Name }
func (m *mcpToolExecutor) Definition() Tool { return m.def }
func (m *mcpToolExecutor) Execute(ctx tool.AgentContext, arguments string) (string, error) {
	return m.client.callTool(m.toolName, arguments)
}

func (a *Agent) StartMCPServers(configs map[string]config.MCPServerConfig) error {
	a.McpClientsMu.Lock()
	a.McpStartErrors = make(map[string]error)
	a.McpClients = make(map[string]*mcpClient)
	a.McpClientsMu.Unlock()

	for name, cfg := range configs {
		mcpHTTPClient := a.HttpClient
		if mcpHTTPClient == nil {
			mcpHTTPClient = &http.Client{
				Timeout: 30 * time.Second,
			}
		} else {
			cCopy := *a.HttpClient
			cCopy.Timeout = 30 * time.Second
			mcpHTTPClient = &cCopy
		}
		jar, _ := cookiejar.New(nil)
		mcpHTTPClient.Jar = jar

		client := &mcpClient{
			name:     name,
			config:   cfg,
			requests: make(map[int64]chan string),
			nextID:   1,
			client:   mcpHTTPClient,
		}

		err := client.start()
		if err != nil {
			a.McpClientsMu.Lock()
			a.McpStartErrors[name] = err
			a.McpClientsMu.Unlock()
			continue
		}

		a.McpClientsMu.Lock()
		a.McpClients[name] = client
		a.McpClientsMu.Unlock()
		fmt.Fprintf(os.Stderr, "Started MCP server '%s'\n", name)

		tools, err := client.listTools()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: Failed to list tools for MCP server '%s': %v\n", name, err)
			continue
		}

		for _, mcpTool := range tools {
			prefixedName := fmt.Sprintf("mcp__%s__%s", name, mcpTool.Name)

			var jsonSchema JSONSchema
			schemaBytes, err := json.Marshal(mcpTool.InputSchema)
			if err == nil {
				_ = json.Unmarshal(schemaBytes, &jsonSchema)
			}

			a.Registry.Register(&mcpToolExecutor{
				client:   client,
				toolName: mcpTool.Name,
				def: Tool{
					Type: "function",
					Function: FunctionDefinition{
						Name:        prefixedName,
						Description: fmt.Sprintf("[%s] %s", name, mcpTool.Description),
						Parameters:  jsonSchema,
					},
				},
			})
		}
	}

	return nil
}

func (a *Agent) StopMCPServers() {
	a.McpClientsMu.Lock()
	defer a.McpClientsMu.Unlock()

	for name, client := range a.McpClients {
		client.close()
		fmt.Fprintf(os.Stderr, "Stopped MCP server '%s'\n", name)
	}
	a.McpClients = make(map[string]*mcpClient)
	a.Registry.UnregisterPrefix("mcp__")
}

func (a *Agent) GetMCPTools() []Tool {
	var allTools []Tool
	for name, t := range a.Registry.GetAllExecutors() {
		if strings.HasPrefix(name, "mcp__") {
			allTools = append(allTools, t.Definition())
		}
	}

	sort.Slice(allTools, func(i, j int) bool {
		return allTools[i].Function.Name < allTools[j].Function.Name
	})

	return allTools
}

func (c *mcpClient) start() error {
	if c.config.URL == "" {
		return fmt.Errorf("MCP server '%s' is missing 'url' config; MCP servers must run independently and only URLs are supported", c.name)
	}
	c.isSSE = true
	return c.startSSE()
}

func (c *mcpClient) startSSE() error {
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel

	req, err := http.NewRequestWithContext(ctx, "GET", c.config.URL, nil)
	if err != nil {
		cancel()
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	for k, v := range c.config.Headers {
		req.Header.Set(k, v)
	}

	sseClient := *c.client
	sseClient.Timeout = 0
	resp, err := sseClient.Do(req)
	if err != nil {
		cancel()
		return err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		cancel()
		return fmt.Errorf("HTTP error %d", resp.StatusCode)
	}

	c.sseBody = resp.Body
	endpointChan := make(chan string, 1)

	go func() {
		scanner := bufio.NewScanner(c.sseBody)
		var lastEvent string
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "event:") {
				lastEvent = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			} else if strings.HasPrefix(line, "data:") {
				data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
				if lastEvent == "endpoint" {
					postURL := data
					if !strings.HasPrefix(postURL, "http://") && !strings.HasPrefix(postURL, "https://") {
						parsedBase, errBase := url.Parse(c.config.URL)
						parsedRel, errRel := url.Parse(postURL)
						if errBase == nil && errRel == nil {
							postURL = parsedBase.ResolveReference(parsedRel).String()
						}
					}
					fmt.Fprintf(os.Stderr, "[MCP] Resolved POST endpoint: %s\n", postURL)
					select {
					case endpointChan <- postURL:
					default:
					}
				} else {
					fmt.Fprintf(os.Stderr, "[MCP Rx] %s\n", data)
					c.handleMessage(data)
				}
				lastEvent = ""
			}
		}
		if err := scanner.Err(); err != nil {
			fmt.Fprintf(os.Stderr, "SSE connection error for '%s': %v\n", c.name, err)
		} else {
			fmt.Fprintf(os.Stderr, "SSE connection closed for '%s'\n", c.name)
		}
	}()

	select {
	case postURL := <-endpointChan:
		c.postURL = postURL
	case <-time.After(5 * time.Second):
		c.close()
		return fmt.Errorf("timeout waiting for SSE endpoint initialization")
	}

	err = c.handshake()
	if err != nil {
		c.close()
		return fmt.Errorf("handshake failed: %w", err)
	}

	return nil
}

func (c *mcpClient) close() {
	if c.cancel != nil {
		c.cancel()
	}
	if c.sseBody != nil {
		c.sseBody.Close()
	}
}

func (c *mcpClient) handleMessage(data string) {
	var msg struct {
		ID     json.RawMessage  `json:"id"`
		Method string           `json:"method"`
		Result *json.RawMessage `json:"result"`
		Error  *json.RawMessage `json:"error"`
	}

	if err := json.Unmarshal([]byte(data), &msg); err != nil {
		return
	}

	var parsedID int64
	hasID := false

	if len(msg.ID) > 0 {
		var idInt int64
		if err := json.Unmarshal(msg.ID, &idInt); err == nil {
			parsedID = idInt
			hasID = true
		} else {
			var idStr string
			if err := json.Unmarshal(msg.ID, &idStr); err == nil {
				if val, err := strconv.ParseInt(idStr, 10, 64); err == nil {
					parsedID = val
					hasID = true
				}
			}
		}
	}

	if hasID {
		c.requestMu.Lock()
		ch, exists := c.requests[parsedID]
		if exists {
			delete(c.requests, parsedID)
			select {
			case ch <- data:
			default:
			}
		}
		c.requestMu.Unlock()
	}
}

func (c *mcpClient) request(method string, params interface{}) (string, error) {
	c.requestMu.Lock()
	id := c.nextID
	c.nextID++
	ch := make(chan string, 1)
	c.requests[id] = ch
	c.requestMu.Unlock()

	reqMap := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
	}
	if params != nil {
		reqMap["params"] = params
	}

	data, err := json.Marshal(reqMap)
	if err != nil {
		c.requestMu.Lock()
		delete(c.requests, id)
		c.requestMu.Unlock()
		return "", err
	}
	fmt.Fprintf(os.Stderr, "[MCP Tx] %s\n", string(data))

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", c.postURL, bytes.NewBuffer(data))
	if err != nil {
		c.requestMu.Lock()
		delete(c.requests, id)
		c.requestMu.Unlock()
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range c.config.Headers {
		req.Header.Set(k, v)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		c.requestMu.Lock()
		delete(c.requests, id)
		c.requestMu.Unlock()
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		c.requestMu.Lock()
		delete(c.requests, id)
		c.requestMu.Unlock()
		return "", fmt.Errorf("HTTP error %d", resp.StatusCode)
	}

	select {
	case res := <-ch:
		return res, nil
	case <-ctx.Done():
		c.requestMu.Lock()
		delete(c.requests, id)
		c.requestMu.Unlock()
		return "", fmt.Errorf("request timeout or cancellation")
	}
}

func (c *mcpClient) handshake() error {
	params := map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo": map[string]string{
			"name":    "maquis-client",
			"version": "1.0.0",
		},
	}

	res, err := c.request("initialize", params)
	if err != nil {
		return err
	}

	var resp struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}

	if err := json.Unmarshal([]byte(res), &resp); err != nil {
		return err
	}

	if resp.Error != nil {
		return fmt.Errorf("MCP error: %s", resp.Error.Message)
	}

	reqMap := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	}
	data, _ := json.Marshal(reqMap)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	fmt.Fprintf(os.Stderr, "[MCP Tx Notification] %s\n", string(data))
	req, err := http.NewRequestWithContext(ctx, "POST", c.postURL, bytes.NewBuffer(data))
	if err == nil {
		req.Header.Set("Content-Type", "application/json")
		for k, v := range c.config.Headers {
			req.Header.Set(k, v)
		}
		if respDrop, errDrop := c.client.Do(req); errDrop == nil {
			if respDrop.StatusCode != http.StatusOK && respDrop.StatusCode != http.StatusAccepted && respDrop.StatusCode != http.StatusNoContent {
				fmt.Fprintf(os.Stderr, "[MCP] notifications/initialized returned status %d\n", respDrop.StatusCode)
			}
			respDrop.Body.Close()
		} else {
			fmt.Fprintf(os.Stderr, "[MCP] failed to send notifications/initialized: %v\n", errDrop)
		}
	}

	c.initialized = true
	return nil
}

func (c *mcpClient) listTools() ([]MCPTool, error) {
	if !c.initialized {
		return nil, fmt.Errorf("client not initialized")
	}

	res, err := c.request("tools/list", nil)
	if err != nil {
		return nil, err
	}

	var resp struct {
		Result struct {
			Tools []MCPTool `json:"tools"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}

	if err := json.Unmarshal([]byte(res), &resp); err != nil {
		return nil, err
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("MCP error: %s", resp.Error.Message)
	}

	return resp.Result.Tools, nil
}

func (c *mcpClient) callTool(name string, arguments string) (string, error) {
	if !c.initialized {
		return "", fmt.Errorf("client not initialized")
	}

	var argsMap map[string]interface{}
	if err := json.Unmarshal([]byte(arguments), &argsMap); err != nil {
		return "", err
	}

	params := map[string]interface{}{
		"name":      name,
		"arguments": argsMap,
	}

	res, err := c.request("tools/call", params)
	if err != nil {
		return "", err
	}

	var resp struct {
		Result struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}

	if err := json.Unmarshal([]byte(res), &resp); err != nil {
		return "", err
	}

	if resp.Error != nil {
		return "", fmt.Errorf("MCP error: %s", resp.Error.Message)
	}

	var output []string
	for _, comp := range resp.Result.Content {
		if comp.Type == "text" {
			output = append(output, comp.Text)
		}
	}

	joined := strings.Join(output, "\n")
	if resp.Result.IsError {
		return joined, fmt.Errorf("tool execution failed")
	}
	return joined, nil
}

func (a *Agent) GetMCPServersStatus() map[string]string {
	a.McpClientsMu.Lock()
	defer a.McpClientsMu.Unlock()

	status := make(map[string]string)
	for name, client := range a.McpClients {
		if client.initialized {
			status[name] = fmt.Sprintf("Connected (URL: %s)", client.config.URL)
		} else {
			status[name] = "Initializing"
		}
	}
	return status
}