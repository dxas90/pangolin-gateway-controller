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
	"strconv"
	"strings"
	"time"

	"github.com/dxas90/pangolin-gateway-controller/pkg/metrics"
)

const (
	// DefaultBaseURL is the default Pangolin API endpoint
	DefaultBaseURL = "https://api.pangolin.net/v1"

	// DefaultTimeout is the default HTTP client timeout
	DefaultTimeout = 30 * time.Second

	// maxResponseBodySize limits how many bytes we read from API responses
	// to prevent memory exhaustion from unexpectedly large payloads.
	maxResponseBodySize = 10 * 1024 * 1024 // 10 MB

	// listPageSize is the number of items to request per page when listing
	// sites or resources. Pangolin 1.16.0+ changed the default from 1000→20;
	// we request 1000 at a time to minimise round-trips while still being
	// safe against deployments with many objects.
	listPageSize = 1000

	// listMaxPages is a safety cap on pagination loops to prevent runaway
	// requests if the server returns unexpectedly large or inconsistent
	// pagination metadata.
	listMaxPages = 100
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
	// Breaker prevents cascading failures when the API is degraded.
	// Opens after 5 consecutive retryable failures, resets after 30 s.
	Breaker *CircuitBreaker
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
			Transport: &http.Transport{
				MaxIdleConnsPerHost: 25,
				IdleConnTimeout:     90 * time.Second,
			},
		},
		Breaker: NewCircuitBreaker(5, 30*time.Second),
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

// doRequest performs an HTTP request with authentication.
// It checks the circuit breaker before executing and records success/failure afterwards.
func (c *Client) doRequest(ctx context.Context, method, path string, body interface{}) ([]byte, error) {
	// Fast-fail if the circuit breaker is open
	if c.Breaker != nil {
		if err := c.Breaker.Allow(); err != nil {
			return nil, err
		}
	}

	startTime := time.Now()

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
		// Connection-level errors: API may be down — count as circuit breaker failure
		if c.Breaker != nil {
			c.Breaker.RecordFailure()
		}
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	// Record metrics
	duration := time.Since(startTime).Seconds()
	statusCode := strconv.Itoa(resp.StatusCode)
	normalizedPath := normalizePath(path)

	metrics.PangolinAPIRequests.WithLabelValues(normalizedPath, method, statusCode).Inc()
	metrics.PangolinAPILatency.WithLabelValues(normalizedPath, method).Observe(duration)

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodySize))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		metrics.PangolinAPIErrors.WithLabelValues(normalizedPath, method, statusCode).Inc()
		apiErr := &PangolinAPIError{
			StatusCode: resp.StatusCode,
			Endpoint:   path,
			Method:     method,
			Message:    string(respBody),
		}
		if c.Breaker != nil {
			if apiErr.IsRetryable() {
				// 5xx/429: API degraded — open the circuit
				c.Breaker.RecordFailure()
			} else {
				// 4xx: API is responsive, application-level error — don't penalise
				c.Breaker.RecordSuccess()
			}
		}
		return nil, apiErr
	}

	// Successful response — API is healthy
	if c.Breaker != nil {
		c.Breaker.RecordSuccess()
	}
	return respBody, nil
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

	// API returns {"data": {...}}
	var response struct {
		Data Site `json:"data"`
	}
	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &response.Data, nil
}

// ListSites lists all sites for the organization, transparently paginating
// through all pages. Pangolin 1.16.0+ changed the default page size from
// 1000 to 20, so we explicitly request listPageSize items per page.
//
// Backward-compatible: older servers treat unknown query params as no-ops
// and still return up to their default limit (1000).
func (c *Client) ListSites(ctx context.Context) ([]Site, error) {
	type paginatedResponse struct {
		Data struct {
			Sites      []Site `json:"sites"`
			Pagination struct {
				Total    int `json:"total"`
				PageSize int `json:"pageSize"`
				Page     int `json:"page"`
				// Legacy fields (Pangolin < 1.16.0)
				Limit  int `json:"limit"`
				Offset int `json:"offset"`
			} `json:"pagination"`
		} `json:"data"`
	}

	var allSites []Site
	basePath := fmt.Sprintf("/org/%s/sites", c.OrgID)

	for page := 1; page <= listMaxPages; page++ {
		path := fmt.Sprintf("%s?pageSize=%d&page=%d", basePath, listPageSize, page)
		respBody, err := c.doRequest(ctx, http.MethodGet, path, nil)
		if err != nil {
			return nil, err
		}

		var resp paginatedResponse
		if err := json.Unmarshal(respBody, &resp); err != nil {
			return nil, fmt.Errorf("failed to unmarshal list sites response (page %d): %w", page, err)
		}

		allSites = append(allSites, resp.Data.Sites...)

		// Determine the effective page size — newer API returns pageSize,
		// older API returns limit; fall back to listPageSize if both are zero.
		effPageSize := resp.Data.Pagination.PageSize
		if effPageSize == 0 {
			effPageSize = resp.Data.Pagination.Limit
		}
		if effPageSize == 0 {
			effPageSize = listPageSize
		}

		// Stop when we've collected all items or the page came back empty.
		if len(resp.Data.Sites) == 0 || len(allSites) >= resp.Data.Pagination.Total {
			break
		}
		// Also stop if the server returned fewer items than requested
		// (last page).
		if len(resp.Data.Sites) < effPageSize {
			break
		}
	}

	return allSites, nil
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

// SetResourceRoles sets the allowed roles for a resource
// Endpoint: POST /resource/{resourceId}/roles
func (c *Client) SetResourceRoles(ctx context.Context, resourceID string, roleIDs []string) error {
	path := fmt.Sprintf("/resource/%s/roles", resourceID)
	payload := map[string]interface{}{
		"roleIds": roleIDs,
	}
	_, err := c.doRequest(ctx, http.MethodPost, path, payload)
	return err
}

// DisableSSO disables SSO for a resource
// Endpoint: POST /resource/{resourceId} with {"sso":false,"skipToIdpId":null}
func (c *Client) DisableSSO(ctx context.Context, resourceID string) error {
	path := fmt.Sprintf("/resource/%s", resourceID)
	payload := map[string]interface{}{
		"sso":         false,
		"skipToIdpId": nil,
	}
	_, err := c.doRequest(ctx, http.MethodPost, path, payload)
	return err
}

// ListDomains lists all domains for the organization, transparently paginating
// through all pages. Uses the same pagination approach as ListSites/ListResources.
// Endpoint: GET /org/{orgId}/domains
func (c *Client) ListDomains(ctx context.Context) ([]map[string]interface{}, error) {
	type paginatedResponse struct {
		Data struct {
			Domains    []map[string]interface{} `json:"domains"`
			Pagination struct {
				Total    int `json:"total"`
				PageSize int `json:"pageSize"`
				Page     int `json:"page"`
				// Legacy fields (Pangolin < 1.16.0)
				Limit  int `json:"limit"`
				Offset int `json:"offset"`
			} `json:"pagination"`
		} `json:"data"`
	}

	var allDomains []map[string]interface{}
	basePath := fmt.Sprintf("/org/%s/domains", c.OrgID)

	for page := 1; page <= listMaxPages; page++ {
		path := fmt.Sprintf("%s?pageSize=%d&page=%d", basePath, listPageSize, page)
		respBody, err := c.doRequest(ctx, http.MethodGet, path, nil)
		if err != nil {
			return nil, err
		}

		var resp paginatedResponse
		if err := json.Unmarshal(respBody, &resp); err != nil {
			return nil, fmt.Errorf("failed to unmarshal list domains response (page %d): %w", page, err)
		}

		allDomains = append(allDomains, resp.Data.Domains...)

		// Determine the effective page size — newer API returns pageSize,
		// older API returns limit; fall back to listPageSize if both are zero.
		effPageSize := resp.Data.Pagination.PageSize
		if effPageSize == 0 {
			effPageSize = resp.Data.Pagination.Limit
		}
		if effPageSize == 0 {
			effPageSize = listPageSize
		}

		// Stop when we've collected all items or the page came back empty.
		if len(resp.Data.Domains) == 0 || len(allDomains) >= resp.Data.Pagination.Total {
			break
		}
		// Also stop if the server returned fewer items than requested
		// (last page).
		if len(resp.Data.Domains) < effPageSize {
			break
		}
	}

	return allDomains, nil
}

// ListResources lists all resources for the organization, transparently
// paginating through all pages. Pangolin 1.16.0+ changed the default page
// size from 1000 to 20, so we explicitly request listPageSize items per page.
//
// Backward-compatible: older servers treat unknown query params as no-ops
// and still return up to their default limit (1000).
func (c *Client) ListResources(ctx context.Context) ([]map[string]interface{}, error) {
	type paginatedResponse struct {
		Data struct {
			Resources  []map[string]interface{} `json:"resources"`
			Pagination struct {
				Total    int `json:"total"`
				PageSize int `json:"pageSize"`
				Page     int `json:"page"`
				// Legacy fields (Pangolin < 1.16.0)
				Limit  int `json:"limit"`
				Offset int `json:"offset"`
			} `json:"pagination"`
		} `json:"data"`
	}

	var allResources []map[string]interface{}
	basePath := fmt.Sprintf("/org/%s/resources", c.OrgID)

	for page := 1; page <= listMaxPages; page++ {
		path := fmt.Sprintf("%s?pageSize=%d&page=%d", basePath, listPageSize, page)
		respBody, err := c.doRequest(ctx, http.MethodGet, path, nil)
		if err != nil {
			return nil, err
		}

		var resp paginatedResponse
		if err := json.Unmarshal(respBody, &resp); err != nil {
			return nil, fmt.Errorf("failed to unmarshal list resources response (page %d): %w", page, err)
		}

		allResources = append(allResources, resp.Data.Resources...)

		// Determine the effective page size — newer API returns pageSize,
		// older API returns limit; fall back to listPageSize if both are zero.
		effPageSize := resp.Data.Pagination.PageSize
		if effPageSize == 0 {
			effPageSize = resp.Data.Pagination.Limit
		}
		if effPageSize == 0 {
			effPageSize = listPageSize
		}

		// Stop when we've collected all items or the page came back empty.
		if len(resp.Data.Resources) == 0 || len(allResources) >= resp.Data.Pagination.Total {
			break
		}
		// Also stop if the server returned fewer items than requested
		// (last page).
		if len(resp.Data.Resources) < effPageSize {
			break
		}
	}

	return allResources, nil
}

// ListTargetsRaw lists all targets for a resource via Integration API,
// transparently paginating through all pages.
// Endpoint: GET /resource/{resourceId}/targets
//
// IMPORTANT: Callers MUST NOT continue with an empty slice when this method
// returns an error. Doing so silently drops all existing targets and can lead
// to incorrect drift-detection or accidental deletion of Pangolin targets.
func (c *Client) ListTargetsRaw(ctx context.Context, resourceID string) ([]map[string]interface{}, error) {
	type paginatedResponse struct {
		Data struct {
			Targets    []map[string]interface{} `json:"targets"`
			Pagination struct {
				Total    int `json:"total"`
				PageSize int `json:"pageSize"`
				Page     int `json:"page"`
				// Legacy fields (Pangolin < 1.16.0)
				Limit  int `json:"limit"`
				Offset int `json:"offset"`
			} `json:"pagination"`
		} `json:"data"`
	}

	var allTargets []map[string]interface{}
	basePath := fmt.Sprintf("/resource/%s/targets", resourceID)

	for page := 1; page <= listMaxPages; page++ {
		path := fmt.Sprintf("%s?pageSize=%d&page=%d", basePath, listPageSize, page)
		respBody, err := c.doRequest(ctx, http.MethodGet, path, nil)
		if err != nil {
			return nil, err
		}

		var resp paginatedResponse
		if err := json.Unmarshal(respBody, &resp); err != nil {
			return nil, fmt.Errorf("failed to unmarshal list targets response (page %d): %w", page, err)
		}

		allTargets = append(allTargets, resp.Data.Targets...)

		// Determine the effective page size — newer API returns pageSize,
		// older API returns limit; fall back to listPageSize if both are zero.
		effPageSize := resp.Data.Pagination.PageSize
		if effPageSize == 0 {
			effPageSize = resp.Data.Pagination.Limit
		}
		if effPageSize == 0 {
			effPageSize = listPageSize
		}

		// Stop when we've collected all items or the page came back empty.
		if len(resp.Data.Targets) == 0 || len(allTargets) >= resp.Data.Pagination.Total {
			break
		}
		// Also stop if the server returned fewer items than requested
		// (last page).
		if len(resp.Data.Targets) < effPageSize {
			break
		}
	}

	return allTargets, nil
}

// GetServerVersion queries the Pangolin server version by performing the same
// newt authentication handshake that the newt VPN client performs on startup.
// The server embeds its version string in the token response.
//
// newtEndpoint is the Pangolin VPN server URL (e.g., "https://pangolin.example.com"),
// newtID and newtSecret are the credentials returned by PickSiteDefaults.
//
// Returns the version string (e.g., "1.15.4") or an error if the call fails.
// A failure is non-fatal — callers should log and continue rather than abort.
func (c *Client) GetServerVersion(ctx context.Context, newtEndpoint, newtID, newtSecret string) (string, error) {
	type tokenRequest struct {
		NewtID string `json:"newtId"`
		Secret string `json:"secret"`
	}
	type tokenResponse struct {
		Data struct {
			Token         string `json:"token"`
			ServerVersion string `json:"serverVersion"`
		} `json:"data"`
		Success bool   `json:"success"`
		Message string `json:"message"`
	}

	payload, err := json.Marshal(tokenRequest{NewtID: newtID, Secret: newtSecret})
	if err != nil {
		return "", fmt.Errorf("failed to marshal token request: %w", err)
	}

	endpoint := strings.TrimRight(newtEndpoint, "/") + "/api/v1/auth/newt/get-token"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewBuffer(payload))
	if err != nil {
		return "", fmt.Errorf("failed to create version request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to reach newt auth endpoint: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodySize))
	if err != nil {
		return "", fmt.Errorf("failed to read token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("newt auth endpoint returned status %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp tokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", fmt.Errorf("failed to decode token response: %w", err)
	}

	if !tokenResp.Success {
		return "", fmt.Errorf("newt auth request rejected: %s", tokenResp.Message)
	}

	return tokenResp.Data.ServerVersion, nil
}

// normalizePath strips query strings and replaces numeric/UUID-like path
// segments with {id} to produce stable metric label values.
// Example: /org/home/sites/12345/resources → /org/{id}/sites/{id}/resources
func normalizePath(path string) string {
	// Strip query string
	if idx := strings.Index(path, "?"); idx >= 0 {
		path = path[:idx]
	}
	// Replace numeric/UUID segments with {id}
	segments := strings.Split(path, "/")
	for i, seg := range segments {
		if isIDSegment(seg) {
			segments[i] = "{id}"
		}
	}
	return strings.Join(segments, "/")
}

func isIDSegment(s string) bool {
	if s == "" {
		return false
	}
	// Pure numeric
	for _, c := range s {
		if c < '0' || c > '9' {
			// Could be UUID - check length and hex chars
			return len(s) >= 8 && isHexString(s)
		}
	}
	return true
}

func isHexString(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') || c == '-') {
			return false
		}
	}
	return true
}
