package pangolin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Ping
// ---------------------------------------------------------------------------

func TestPing_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Ping uses the org sites endpoint; any non-5xx response is "up"
		w.WriteHeader(http.StatusUnauthorized) // 401 still means reachable
	}))
	defer srv.Close()

	c := newTestClient(srv)
	assert.NoError(t, c.Ping(context.Background()))
}

func TestPing_ServerError_Returns500(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := newTestClient(srv)
	err := c.Ping(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "500")
}

func TestPing_ConnectionError(t *testing.T) {
	c := NewClient("key", "org")
	c.BaseURL = "http://127.0.0.1:1" // nothing listening
	c.HTTPClient = &http.Client{Timeout: 100 * time.Millisecond}
	c.Breaker = NewCircuitBreaker(5, 30*time.Second)

	err := c.Ping(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unreachable")
}

func TestPing_OpenCircuitBreaker_ReturnsFastFail(t *testing.T) {
	// Open the breaker first
	c := NewClient("key", "org")
	c.BaseURL = "http://127.0.0.1:1"
	c.Breaker = NewCircuitBreaker(1, 10*time.Minute)
	c.Breaker.RecordFailure() // opens immediately (threshold=1)

	err := c.Ping(context.Background())
	assert.ErrorIs(t, err, ErrCircuitOpen, "Ping must fast-fail when breaker is open")
}

func TestPing_NilBreaker_Works(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient(srv)
	c.Breaker = nil
	assert.NoError(t, c.Ping(context.Background()))
}

// ---------------------------------------------------------------------------
// doRequest — circuit-open blocks all methods
// ---------------------------------------------------------------------------

func TestDoRequest_OpenCircuit_BlocksCreateSite(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient(srv)
	c.Breaker = NewCircuitBreaker(1, 10*time.Minute)
	c.Breaker.RecordFailure() // open

	_, err := c.CreateSite(context.Background(), &Site{Name: "test", Type: "newt"})
	assert.ErrorIs(t, err, ErrCircuitOpen)
	assert.Equal(t, 0, calls, "no HTTP call must be made when circuit is open")
}

func TestDoRequest_OpenCircuit_BlocksDeleteResource(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	c.Breaker = NewCircuitBreaker(1, 10*time.Minute)
	c.Breaker.RecordFailure()

	err := c.DeleteResource(context.Background(), "res-1")
	assert.ErrorIs(t, err, ErrCircuitOpen)
	assert.Equal(t, 0, calls)
}

// ---------------------------------------------------------------------------
// doRequest — large response body truncation (metrics path, not panic)
// ---------------------------------------------------------------------------

func TestDoRequest_LargeErrorBody_Truncated(t *testing.T) {
	bigBody := strings.Repeat("x", 1024) // > 512 bytes triggers truncation in error message
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(bigBody))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	_, err := c.ListSites(context.Background())
	require.Error(t, err)
	apiErr, ok := AsAPIError(err)
	require.True(t, ok)
	assert.Contains(t, apiErr.Message, "[truncated]")
	assert.LessOrEqual(t, len(apiErr.Message), 600, "message must be bounded")
}

func TestDoRequest_ExactlyAtTruncationBoundary_NoTruncation(t *testing.T) {
	body512 := strings.Repeat("a", 512) // exactly 512 bytes — should NOT be truncated
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(body512))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	_, err := c.ListSites(context.Background())
	require.Error(t, err)
	apiErr, ok := AsAPIError(err)
	require.True(t, ok)
	assert.NotContains(t, apiErr.Message, "[truncated]")
}

// ---------------------------------------------------------------------------
// doRequest — 429 is retryable and opens circuit
// ---------------------------------------------------------------------------

func TestDoRequest_429_OpenCircuit(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate limited"}`))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	c.Breaker = NewCircuitBreaker(1, 10*time.Minute)

	_, err := c.ListSites(context.Background())
	require.Error(t, err)
	assert.Equal(t, "open", c.Breaker.State(), "429 is retryable; circuit must open after 1 failure")
}

// ---------------------------------------------------------------------------
// doRequest — RecordSuccess on 4xx (application errors must not penalise breaker)
// ---------------------------------------------------------------------------

func TestDoRequest_404_DoesNotOpenCircuit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not found"}`))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	c.Breaker = NewCircuitBreaker(1, 10*time.Minute)

	_ = c.DeleteResource(context.Background(), "missing")
	assert.Equal(t, "closed", c.Breaker.State())
}

// ---------------------------------------------------------------------------
// doRequest — connection failure increments breaker
// ---------------------------------------------------------------------------

func TestDoRequest_ConnectionFailure_IncrementsBreakerAndOpens(t *testing.T) {
	c := NewClient("key", "org")
	c.BaseURL = "http://127.0.0.1:1"
	c.HTTPClient = &http.Client{Timeout: 50 * time.Millisecond}
	c.Breaker = NewCircuitBreaker(1, 10*time.Minute)

	_, err := c.ListSites(context.Background())
	require.Error(t, err)
	assert.Equal(t, "open", c.Breaker.State(), "connection error must open circuit after threshold")
}

// ---------------------------------------------------------------------------
// ListTargetsRaw — multi-page pagination
// ---------------------------------------------------------------------------

func TestListTargetsRaw_MultiPage(t *testing.T) {
	page0 := map[string]interface{}{
		"data": map[string]interface{}{
			"targets": []map[string]interface{}{
				{"targetId": "t1", "ip": "10.0.0.1"},
				{"targetId": "t2", "ip": "10.0.0.2"},
			},
			"pagination": map[string]interface{}{
				"total": float64(3),
				"limit": float64(2),
			},
		},
	}
	page1 := map[string]interface{}{
		"data": map[string]interface{}{
			"targets": []map[string]interface{}{
				{"targetId": "t3", "ip": "10.0.0.3"},
			},
			"pagination": map[string]interface{}{
				"total": float64(3),
				"limit": float64(2),
			},
		},
	}

	reqCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { reqCount++ }()
		var payload interface{}
		if reqCount == 0 {
			payload = page0
		} else {
			payload = page1
		}
		writeJSON(w, http.StatusOK, payload)
	}))
	defer srv.Close()

	c := newTestClient(srv)
	targets, err := c.ListTargetsRaw(context.Background(), "res-1")
	require.NoError(t, err)
	require.Len(t, targets, 3)
	assert.Equal(t, "t1", targets[0]["targetId"])
	assert.Equal(t, "t3", targets[2]["targetId"])
}

func TestListTargetsRaw_EmptyResponse_ReturnsEmptySlice(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"data": map[string]interface{}{
				"targets":    []map[string]interface{}{},
				"pagination": map[string]interface{}{"total": float64(0), "limit": float64(100)},
			},
		})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	targets, err := c.ListTargetsRaw(context.Background(), "res-empty")
	require.NoError(t, err)
	assert.Empty(t, targets)
}

// ---------------------------------------------------------------------------
// ListResources — legacy pagination (limit/offset fields)
// ---------------------------------------------------------------------------

func TestListResources_LegacyPagination(t *testing.T) {
	page := map[string]interface{}{
		"data": map[string]interface{}{
			"resources": []map[string]interface{}{
				{"resourceId": "r1"},
				{"resourceId": "r2"},
			},
			"pagination": map[string]interface{}{
				// PageSize is zero (old server), limit is non-zero
				"total":    float64(2),
				"pageSize": float64(0),
				"limit":    float64(1000),
			},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, page)
	}))
	defer srv.Close()

	c := newTestClient(srv)
	resources, err := c.ListResources(context.Background())
	require.NoError(t, err)
	require.Len(t, resources, 2)
}

func TestListResources_StopsOnLastPage_WhenFewerItemsReturned(t *testing.T) {
	// Server returns 3 items when limit is 1000 — pagination loop must stop after page 1.
	reqCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqCount++
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"data": map[string]interface{}{
				"resources": []map[string]interface{}{
					{"resourceId": "r1"},
					{"resourceId": "r2"},
					{"resourceId": "r3"},
				},
				"pagination": map[string]interface{}{
					"total":    float64(3),
					"pageSize": float64(1000),
				},
			},
		})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	resources, err := c.ListResources(context.Background())
	require.NoError(t, err)
	assert.Len(t, resources, 3)
	assert.Equal(t, 1, reqCount, "must stop after a single page when items < pageSize")
}

// ---------------------------------------------------------------------------
// SiteResource ResponseHeaders — new 1.24.0 field is serialised correctly
// ---------------------------------------------------------------------------

func TestSiteResource_ResponseHeaders_Serialisation(t *testing.T) {
	sr := SiteResource{
		Name:     "test",
		SiteID:   "site-1",
		Type:     "http",
		Address:  "10.0.0.1",
		Port:     80,
		Protocol: "http",
		ResponseHeaders: map[string]string{
			"X-Frame-Options": "DENY",
			"X-Custom-Header": "value",
		},
	}
	b, err := json.Marshal(sr)
	require.NoError(t, err)
	assert.Contains(t, string(b), `"responseHeaders"`)
	assert.Contains(t, string(b), "DENY")
}

func TestSiteResource_ResponseHeaders_OmittedWhenNil(t *testing.T) {
	sr := SiteResource{
		Name:     "no-headers",
		SiteID:   "site-1",
		Type:     "http",
		Address:  "10.0.0.1",
		Port:     80,
		Protocol: "http",
	}
	b, err := json.Marshal(sr)
	require.NoError(t, err)
	assert.NotContains(t, string(b), "responseHeaders", "omitempty: field must be absent when nil")
}

// ---------------------------------------------------------------------------
// RuleCondition — method condition type round-trips through JSON
// ---------------------------------------------------------------------------

func TestRuleCondition_MethodType_RoundTrip(t *testing.T) {
	rc := RuleCondition{
		Type:     "method",
		Operator: "equals",
		Value:    "GET",
	}
	b, err := json.Marshal(rc)
	require.NoError(t, err)
	assert.Contains(t, string(b), `"method"`)

	var got RuleCondition
	require.NoError(t, json.Unmarshal(b, &got))
	assert.Equal(t, "method", got.Type)
	assert.Equal(t, "GET", got.Value)
}

// ---------------------------------------------------------------------------
// normalizePath — additional corner cases
// ---------------------------------------------------------------------------

func TestNormalizePath_PublicResourceEndpoints(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		// Numeric IDs are normalised
		{"/public-resource/12345/targets", "/public-resource/{id}/targets"},
		{"/public-resource/12345/target", "/public-resource/{id}/target"},
		{"/public-resource/12345/rule/67890", "/public-resource/{id}/rule/{id}"},
		{"/public-resource/12345/rules", "/public-resource/{id}/rules"},
		{"/public-resource/12345/roles", "/public-resource/{id}/roles"},
		// UUID-style IDs are normalised
		{"/public-resource/abcdef12-3456-7890-abcd-ef1234567890/target",
			"/public-resource/{id}/target"},
		// Org paths with non-ID slugs are left unchanged
		{"/org/my-org/public-resource", "/org/my-org/public-resource"},
		{"/org/my-org/public-resources", "/org/my-org/public-resources"},
		// Query strings are stripped before normalisation
		{"/public-resource/12345/targets?limit=100&offset=0", "/public-resource/{id}/targets"},
		// Short alphanumeric slug (not pure numeric, not UUID) is NOT collapsed
		{"/public-resource/abc123/targets", "/public-resource/abc123/targets"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.expected, normalizePath(tt.input))
		})
	}
}

// ---------------------------------------------------------------------------
// ListSites — stops when total is reached mid-page
// ---------------------------------------------------------------------------

func TestListSites_StopsWhenTotalReached(t *testing.T) {
	reqCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqCount++
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"data": map[string]interface{}{
				"sites": []map[string]interface{}{
					{"siteId": float64(1), "name": "site-a"},
					{"siteId": float64(2), "name": "site-b"},
				},
				"pagination": map[string]interface{}{
					"total":    float64(2), // total == len(sites): stop now
					"pageSize": float64(1000),
					"page":     float64(1),
				},
			},
		})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	sites, err := c.ListSites(context.Background())
	require.NoError(t, err)
	assert.Len(t, sites, 2)
	assert.Equal(t, 1, reqCount)
}

// ---------------------------------------------------------------------------
// GetSite — path uses string siteID
// ---------------------------------------------------------------------------

func TestGetSite_PathContainsSiteID(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"data": map[string]interface{}{
				"siteId": float64(99),
				"name":   "test-site",
				"type":   "newt",
			},
		})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	site, err := c.GetSite(context.Background(), "99")
	require.NoError(t, err)
	assert.Equal(t, "/site/99", gotPath)
	assert.Equal(t, "test-site", site.Name)
}

// ---------------------------------------------------------------------------
// DeleteSite — uses numeric ID in path
// ---------------------------------------------------------------------------

func TestDeleteSite_PathContainsNumericID(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	err := c.DeleteSite(context.Background(), 12345)
	require.NoError(t, err)
	assert.Equal(t, "/site/12345", gotPath)
}

// ---------------------------------------------------------------------------
// CreateResource — PUT method and correct path
// ---------------------------------------------------------------------------

func TestCreateResource_UsesPublicResourcePath(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"data": map[string]interface{}{"resourceId": "new-res"},
		})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	result, err := c.CreateResource(context.Background(), map[string]interface{}{"name": "example.com", "mode": "http"})
	require.NoError(t, err)
	assert.Equal(t, http.MethodPut, gotMethod)
	assert.Equal(t, fmt.Sprintf("/org/%s/public-resource", c.OrgID), gotPath)
	assert.Equal(t, "new-res", result["resourceId"])
}

// ---------------------------------------------------------------------------
// UpdateResource — POST method
// ---------------------------------------------------------------------------

func TestUpdateResource_UsesPostMethod(t *testing.T) {
	var gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	err := c.UpdateResource(context.Background(), "res-1", map[string]interface{}{"ssl": true})
	require.NoError(t, err)
	assert.Equal(t, http.MethodPost, gotMethod)
}

// ---------------------------------------------------------------------------
// DisableSSO — sends correct payload
// ---------------------------------------------------------------------------

func TestDisableSSO_SendsCorrectPayload(t *testing.T) {
	var body map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	err := c.DisableSSO(context.Background(), "res-sso")
	require.NoError(t, err)
	assert.Equal(t, false, body["sso"])
	assert.Nil(t, body["skipToIdpId"])
}

// ---------------------------------------------------------------------------
// SetResourceRoles — sends correct payload
// ---------------------------------------------------------------------------

func TestSetResourceRoles_SendsRoleIDs(t *testing.T) {
	var body map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	err := c.SetResourceRoles(context.Background(), "res-1", []string{"role-a", "role-b"})
	require.NoError(t, err)
	ids, ok := body["roleIds"].([]interface{})
	require.True(t, ok)
	assert.Len(t, ids, 2)
}

// ---------------------------------------------------------------------------
// GetServerVersion — response body path
// ---------------------------------------------------------------------------

func TestGetServerVersion_PathAndMethod(t *testing.T) {
	var gotPath, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"data":    map[string]interface{}{"token": "tok", "serverVersion": "1.24.0"},
			"success": true,
		})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	ver, err := c.GetServerVersion(context.Background(), srv.URL, "nid", "secret")
	require.NoError(t, err)
	assert.Equal(t, "1.24.0", ver)
	assert.Equal(t, "/api/v1/auth/newt/get-token", gotPath)
	assert.Equal(t, http.MethodPost, gotMethod)
}

func TestGetServerVersion_Non200Response(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("down"))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	_, err := c.GetServerVersion(context.Background(), srv.URL, "nid", "secret")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "503")
}
