// Package pangolin provides a client for the Pangolin Integration API.
// The client handles REST API communication with self-hosted Pangolin instances
// and manages newt-type sites for WireGuard VPN connectivity.
package pangolin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	// DefaultBaseURL is the default Pangolin API endpoint
	DefaultBaseURL = "https://api.pangolin.net/v1"

	// DefaultTimeout is the default HTTP client timeout
	DefaultTimeout = 30 * time.Second
)

// Client is a REST API client for the Pangolin Integration API.
// It uses Bearer token authentication and supports operations on Sites,
// SiteResources, Targets, and Rules. Note: The Integration API has
// limited functionality compared to the cloud API.
type Client struct {
	BaseURL    string
	APIKey     string
	OrgID      string
	HTTPClient *http.Client
}

// NewClient creates a new Pangolin API client with the given credentials.
// The API key and organization ID are required for all API operations.
//
// Example:
//
//	client := pangolin.NewClient("your-api-key", "your-org-id")
//	client.BaseURL = "https://api.example.com/v1" // For self-hosted instances
func NewClient(apiKey, orgID string) *Client {
	return &Client{
		BaseURL: DefaultBaseURL,
		APIKey:  apiKey,
		OrgID:   orgID,
		HTTPClient: &http.Client{
			Timeout: DefaultTimeout,
		},
	}
}

// SiteResource represents a Pangolin site resource
type SiteResource struct {
	ID          string            `json:"id,omitempty"`
	Name        string            `json:"name"`
	SiteID      string            `json:"siteId"`
	Type        string            `json:"type"`
	Address     string            `json:"address"`
	Port        int               `json:"port"`
	Protocol    string            `json:"protocol"`
	Labels      map[string]string `json:"labels,omitempty"`
	Status      string            `json:"status,omitempty"`
	HealthCheck *HealthCheck      `json:"healthCheck,omitempty"`
}

// HealthCheck represents health check configuration
type HealthCheck struct {
	Enabled  bool   `json:"enabled"`
	Path     string `json:"path,omitempty"`
	Interval int    `json:"interval,omitempty"`
	Timeout  int    `json:"timeout,omitempty"`
}

// Target represents a routing target
type Target struct {
	ID         string            `json:"id,omitempty"`
	ResourceID string            `json:"resourceId"`
	Hostname   string            `json:"hostname"`
	Port       int               `json:"port"`
	Protocol   string            `json:"protocol"`
	Path       string            `json:"path,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
}

// Rule represents a routing rule
type Rule struct {
	ID         string            `json:"id,omitempty"`
	ResourceID string            `json:"resourceId"`
	Priority   int               `json:"priority"`
	Conditions []RuleCondition   `json:"conditions"`
	Actions    []RuleAction      `json:"actions"`
	Labels     map[string]string `json:"labels,omitempty"`
}

// RuleCondition represents a rule matching condition
type RuleCondition struct {
	Type     string `json:"type"`     // "path", "header", "method", "host"
	Operator string `json:"operator"` // "equals", "prefix", "regex"
	Value    string `json:"value"`
	Key      string `json:"key,omitempty"` // For header matching
}

// RuleAction represents a rule action
type RuleAction struct {
	Type   string                 `json:"type"` // "route", "redirect", "rewrite"
	Config map[string]interface{} `json:"config"`
}

// Site represents a Pangolin site
type Site struct {
	ID         int    `json:"siteId,omitempty"`
	OrgID      string `json:"orgId,omitempty"`
	Name       string `json:"name"`
	Type       string `json:"type,omitempty"` // "newt", "wireguard", or "local"
	NiceID     string `json:"niceId,omitempty"`
	Subnet     string `json:"subnet,omitempty"`
	Address    string `json:"address,omitempty"`
	ExitNodeID int    `json:"exitNodeId,omitempty"`
	Secret     string `json:"secret,omitempty"`
	NewtID     string `json:"newtId,omitempty"`
	Online     bool   `json:"online,omitempty"`
	PubKey     string `json:"pubKey,omitempty"`
}

// SiteDefaults represents default values for creating a new site
type SiteDefaults struct {
	ExitNodeID    int    `json:"exitNodeId"`
	Address       string `json:"address"`
	Subnet        string `json:"subnet"`
	ClientAddress string `json:"clientAddress"`
	NewtID        string `json:"newtId"`
	NewtSecret    string `json:"newtSecret"`
	PublicKey     string `json:"publicKey"`
	Endpoint      string `json:"endpoint"`
	ListenPort    int    `json:"listenPort"`
}

// doRequest performs an HTTP request with authentication
func (c *Client) doRequest(ctx context.Context, method, path string, body interface{}) ([]byte, error) {
	var reqBody io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		reqBody = bytes.NewBuffer(jsonBody)
	}

	url := c.BaseURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	return respBody, nil
}

// CreateSiteResource creates a new site resource
func (c *Client) CreateSiteResource(ctx context.Context, resource *SiteResource) (*SiteResource, error) {
	path := fmt.Sprintf("/org/%s/site-resource", c.OrgID)
	respBody, err := c.doRequest(ctx, http.MethodPut, path, resource)
	if err != nil {
		return nil, err
	}

	// API returns {"data": {...}}
	var response struct {
		Data SiteResource `json:"data"`
	}
	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &response.Data, nil
}

// GetSiteResource retrieves a site resource by ID
func (c *Client) GetSiteResource(ctx context.Context, resourceID string) (*SiteResource, error) {
	path := fmt.Sprintf("/site-resource/%s", resourceID)
	respBody, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}

	// API returns {"data": {...}}
	var response struct {
		Data SiteResource `json:"data"`
	}
	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &response.Data, nil
}

// UpdateSiteResource updates an existing site resource
func (c *Client) UpdateSiteResource(ctx context.Context, resourceID string, resource *SiteResource) (*SiteResource, error) {
	path := fmt.Sprintf("/site-resource/%s", resourceID)
	respBody, err := c.doRequest(ctx, http.MethodPost, path, resource)
	if err != nil {
		return nil, err
	}

	var result SiteResource
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &result, nil
}

// DeleteSiteResource deletes a site resource
func (c *Client) DeleteSiteResource(ctx context.Context, resourceID string) error {
	path := fmt.Sprintf("/site-resource/%s", resourceID)
	_, err := c.doRequest(ctx, http.MethodDelete, path, nil)
	return err
}

// DeleteResource deletes a resource via Integration API
// Endpoint: DELETE /resource/{resourceId}
func (c *Client) DeleteResource(ctx context.Context, resourceID string) error {
	path := fmt.Sprintf("/resource/%s", resourceID)
	_, err := c.doRequest(ctx, http.MethodDelete, path, nil)
	return err
}

// UpdateResource updates a resource via Integration API (for headers, SSL settings, etc.)
// Endpoint: POST /resource/{resourceId}
func (c *Client) UpdateResource(ctx context.Context, resourceID string, data map[string]interface{}) error {
	path := fmt.Sprintf("/resource/%s", resourceID)
	_, err := c.doRequest(ctx, http.MethodPost, path, data)
	return err
}

// CreateTarget creates a new routing target
func (c *Client) CreateTarget(ctx context.Context, resourceID string, target *Target) (*Target, error) {
	path := fmt.Sprintf("/resource/%s/target", resourceID)
	respBody, err := c.doRequest(ctx, http.MethodPut, path, target)
	if err != nil {
		return nil, err
	}

	// API returns {"data": {...}}
	var response struct {
		Data Target `json:"data"`
	}
	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &response.Data, nil
}

// ListTargets lists all targets for a resource
func (c *Client) ListTargets(ctx context.Context, resourceID string) ([]Target, error) {
	path := fmt.Sprintf("/resource/%s/targets", resourceID)
	respBody, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}

	// API returns {"data": {"targets": [...]}}
	var response struct {
		Data struct {
			Targets []Target `json:"targets"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return response.Data.Targets, nil
}

// DeleteTarget deletes a target
func (c *Client) DeleteTarget(ctx context.Context, targetID string) error {
	path := fmt.Sprintf("/target/%s", targetID)
	_, err := c.doRequest(ctx, http.MethodDelete, path, nil)
	return err
}

// CreateRule creates a new routing rule
func (c *Client) CreateRule(ctx context.Context, resourceID string, rule *Rule) (*Rule, error) {
	path := fmt.Sprintf("/resource/%s/rule", resourceID)
	respBody, err := c.doRequest(ctx, http.MethodPut, path, rule)
	if err != nil {
		return nil, err
	}

	// API returns {"data": {...}}
	var response struct {
		Data Rule `json:"data"`
	}
	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &response.Data, nil
}

// ListRules lists all rules for a resource
func (c *Client) ListRules(ctx context.Context, resourceID string) ([]Rule, error) {
	path := fmt.Sprintf("/resource/%s/rules", resourceID)
	respBody, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}

	// API returns {"data": {"rules": [...]}}
	var response struct {
		Data struct {
			Rules []Rule `json:"rules"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return response.Data.Rules, nil
}

// DeleteRule deletes a routing rule
func (c *Client) DeleteRule(ctx context.Context, resourceID, ruleID string) error {
	path := fmt.Sprintf("/resource/%s/rule/%s", resourceID, ruleID)
	_, err := c.doRequest(ctx, http.MethodDelete, path, nil)
	return err
}

// CreateSite creates a new site
func (c *Client) CreateSite(ctx context.Context, site *Site) (*Site, error) {
	path := fmt.Sprintf("/org/%s/site", c.OrgID)
	respBody, err := c.doRequest(ctx, http.MethodPut, path, site)
	if err != nil {
		return nil, err
	}

	// API returns {"data": {...}}
	var response struct {
		Data Site `json:"data"`
	}
	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &response.Data, nil
}

// GetSite retrieves a site by ID
func (c *Client) GetSite(ctx context.Context, siteID string) (*Site, error) {
	path := fmt.Sprintf("/site/%s", siteID)
	respBody, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}

	var result Site
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &result, nil
}

// ListSites lists all sites for the organization
func (c *Client) ListSites(ctx context.Context) ([]Site, error) {
	path := fmt.Sprintf("/org/%s/sites", c.OrgID)
	respBody, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}

	// API returns {"data": {"sites": [...]}}
	var response struct {
		Data struct {
			Sites []Site `json:"sites"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return response.Data.Sites, nil
}

// DeleteSite deletes a site by ID
func (c *Client) DeleteSite(ctx context.Context, siteID int) error {
	path := fmt.Sprintf("/site/%d", siteID)
	_, err := c.doRequest(ctx, http.MethodDelete, path, nil)
	return err
}

// PickSiteDefaults retrieves default values for creating a new site
func (c *Client) PickSiteDefaults(ctx context.Context) (*SiteDefaults, error) {
	path := fmt.Sprintf("/org/%s/pick-site-defaults", c.OrgID)
	respBody, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}

	// API returns {"data": {...}}
	var response struct {
		Data SiteDefaults `json:"data"`
	}
	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &response.Data, nil
}

// CreateResource creates a new resource via Integration API
// Endpoint: PUT /org/{orgId}/resource
func (c *Client) CreateResource(ctx context.Context, resourceData map[string]interface{}) (map[string]interface{}, error) {
	path := fmt.Sprintf("/org/%s/resource", c.OrgID)
	respBody, err := c.doRequest(ctx, http.MethodPut, path, resourceData)
	if err != nil {
		return nil, err
	}

	// API returns {"data": {...}}
	var response struct {
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return response.Data, nil
}

// CreateTargetRaw creates a new target via Integration API
// Endpoint: PUT /resource/{resourceId}/target
func (c *Client) CreateTargetRaw(ctx context.Context, resourceID string, targetData map[string]interface{}) (map[string]interface{}, error) {
	path := fmt.Sprintf("/resource/%s/target", resourceID)
	respBody, err := c.doRequest(ctx, http.MethodPut, path, targetData)
	if err != nil {
		return nil, err
	}

	// API returns {"data": {...}}
	var response struct {
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return response.Data, nil
}

// DisableSSO disables SSO for a resource by setting empty roles
// Endpoint: POST /resource/{resourceId} with {"roleIds":[]}
func (c *Client) DisableSSO(ctx context.Context, resourceID string, patchData map[string]interface{}) error {
	path := fmt.Sprintf("/resource/%s", resourceID)
	_, err := c.doRequest(ctx, http.MethodPost, path, patchData)
	return err
}

// ListDomains lists all domains for the organization
// Endpoint: GET /org/{orgId}/domains
func (c *Client) ListDomains(ctx context.Context) ([]map[string]interface{}, error) {
	path := fmt.Sprintf("/org/%s/domains", c.OrgID)
	respBody, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}

	// API returns {"data": {"domains": [...]}}
	var response struct {
		Data struct {
			Domains []map[string]interface{} `json:"domains"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return response.Data.Domains, nil
}

// ListResources lists all resources for the organization
// Endpoint: GET /org/{orgId}/resources
func (c *Client) ListResources(ctx context.Context) ([]map[string]interface{}, error) {
	path := fmt.Sprintf("/org/%s/resources", c.OrgID)
	respBody, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}

	// API returns {"data": {"resources": [...]}}
	var response struct {
		Data struct {
			Resources []map[string]interface{} `json:"resources"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return response.Data.Resources, nil
}

// ListTargetsRaw lists all targets for a resource via Integration API
// Endpoint: GET /resource/{resourceId}/targets
func (c *Client) ListTargetsRaw(ctx context.Context, resourceID string) ([]map[string]interface{}, error) {
	path := fmt.Sprintf("/resource/%s/targets", resourceID)
	respBody, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}

	// API returns {"data": {"targets": [...]}}
	var response struct {
		Data struct {
			Targets []map[string]interface{} `json:"targets"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return response.Data.Targets, nil
}
