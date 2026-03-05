package pangolin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestClient creates a Client pointed at the given test server.
func newTestClient(srv *httptest.Server) *Client {
	c := NewClient("test-api-key", "test-org")
	c.BaseURL = srv.URL
	c.HTTPClient = srv.Client()
	return c
}

// writeJSON is a helper for test handlers.
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// --- NewClient ---

func TestNewClient_Defaults(t *testing.T) {
	c := NewClient("key", "org")
	assert.Equal(t, DefaultBaseURL, c.BaseURL)
	assert.Equal(t, "key", c.APIKey)
	assert.Equal(t, "org", c.OrgID)
	assert.NotNil(t, c.Breaker)
	assert.Equal(t, "closed", c.Breaker.State())
}

// --- Authorization header ---

func TestDoRequest_SetsAuthorizationHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		writeJSON(w, http.StatusOK, map[string]interface{}{"data": map[string]interface{}{}})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	_, err := c.ListResources(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Bearer test-api-key", gotAuth)
}

// --- PickSiteDefaults ---

func TestPickSiteDefaults_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Contains(t, r.URL.Path, "/pick-site-defaults")
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"data": map[string]interface{}{
				"exitNodeId": 1,
				"subnet":     "100.64.0.0/24",
				"newtId":     "newt-001",
				"newtSecret": "s3cr3t",
			},
		})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	defaults, err := c.PickSiteDefaults(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "newt-001", defaults.NewtID)
	assert.Equal(t, "s3cr3t", defaults.NewtSecret)
}

// --- CreateSite ---

func TestCreateSite_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPut, r.Method)
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"data": map[string]interface{}{"siteId": 42, "name": "pgc-test"},
		})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	site, err := c.CreateSite(context.Background(), &Site{Name: "pgc-test", Type: "newt"})
	require.NoError(t, err)
	assert.Equal(t, 42, site.ID)
}

// --- ListSites ---

func TestListSites_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"data": map[string]interface{}{
				"sites": []map[string]interface{}{
					{"siteId": 1, "name": "site-a"},
					{"siteId": 2, "name": "site-b"},
				},
			},
		})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	sites, err := c.ListSites(context.Background())
	require.NoError(t, err)
	assert.Len(t, sites, 2)
	assert.Equal(t, "site-a", sites[0].Name)
}

// --- DeleteSite ---

func TestDeleteSite_Success(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodDelete, r.Method)
		assert.Contains(t, r.URL.Path, "/site/42")
		called = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	err := c.DeleteSite(context.Background(), 42)
	require.NoError(t, err)
	assert.True(t, called)
}

// --- CreateResource ---

func TestCreateResource_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPut, r.Method)
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"data": map[string]interface{}{"resourceId": "res-123", "name": "test.example.com"},
		})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	result, err := c.CreateResource(context.Background(), map[string]interface{}{
		"name": "test.example.com", "http": true,
	})
	require.NoError(t, err)
	assert.Equal(t, "res-123", result["resourceId"])
}

// --- ListResources ---

func TestListResources_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"data": map[string]interface{}{
				"resources": []map[string]interface{}{
					{"resourceId": "r1", "name": "res-one"},
				},
			},
		})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	resources, err := c.ListResources(context.Background())
	require.NoError(t, err)
	require.Len(t, resources, 1)
	assert.Equal(t, "r1", resources[0]["resourceId"])
}

// --- DeleteResource ---

func TestDeleteResource_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodDelete, r.Method)
		assert.Contains(t, r.URL.Path, "/resource/res-99")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	err := c.DeleteResource(context.Background(), "res-99")
	require.NoError(t, err)
}

// --- CreateTargetRaw ---

func TestCreateTargetRaw_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPut, r.Method)
		assert.Contains(t, r.URL.Path, "/resource/res-1/target")
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"data": map[string]interface{}{"targetId": "tgt-001"},
		})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	result, err := c.CreateTargetRaw(context.Background(), "res-1", map[string]interface{}{
		"ip": "10.0.0.1", "port": 80,
	})
	require.NoError(t, err)
	assert.Equal(t, "tgt-001", result["targetId"])
}

// --- ListTargetsRaw ---

func TestListTargetsRaw_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"data": map[string]interface{}{
				"targets": []map[string]interface{}{
					{"targetId": "t1", "ip": "10.0.0.1", "port": float64(80)},
				},
			},
		})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	targets, err := c.ListTargetsRaw(context.Background(), "res-1")
	require.NoError(t, err)
	require.Len(t, targets, 1)
	assert.Equal(t, "t1", targets[0]["targetId"])
}

// --- DeleteTarget ---

func TestDeleteTarget_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodDelete, r.Method)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	err := c.DeleteTarget(context.Background(), "tgt-9")
	require.NoError(t, err)
}

// --- Error handling ---

func TestDoRequest_404_ReturnsPangolinAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not found"}`))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	_, err := c.ListResources(context.Background())
	require.Error(t, err)

	apiErr, ok := AsPangolinAPIError(err)
	require.True(t, ok)
	assert.Equal(t, 404, apiErr.StatusCode)
	assert.True(t, apiErr.IsNotFound())
}

func TestDoRequest_5xx_OpenCircuitBreaker(t *testing.T) {
	call := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call++
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"unavailable"}`))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	c.Breaker = NewCircuitBreaker(3, 1*time.Minute) // Threshold 3

	for i := 0; i < 3; i++ {
		_, err := c.ListResources(context.Background())
		require.Error(t, err)
	}

	// Circuit should now be open
	assert.Equal(t, "open", c.Breaker.State())
	assert.Equal(t, 3, call) // Exactly 3 API calls made

	// Next call should be fast-failed without hitting server
	_, err := c.ListResources(context.Background())
	assert.ErrorIs(t, err, ErrCircuitOpen)
	assert.Equal(t, 3, call) // Still 3, no new request
}

func TestDoRequest_4xx_DoesNotOpenCircuitBreaker(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	c.Breaker = NewCircuitBreaker(3, 1*time.Minute)

	// 4xx responses should not open the circuit (RecordSuccess on 4xx)
	for i := 0; i < 5; i++ {
		_, _ = c.ListResources(context.Background())
	}
	assert.Equal(t, "closed", c.Breaker.State())
}

// --- ListDomains ---

func TestListDomains_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.Path, "/domains")
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"data": map[string]interface{}{
				"domains": []map[string]interface{}{
					{"domainId": "dom-1", "name": "example.com"},
				},
			},
		})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	domains, err := c.ListDomains(context.Background())
	require.NoError(t, err)
	require.Len(t, domains, 1)
	assert.Equal(t, "dom-1", domains[0]["domainId"])
}

// --- DisableSSO ---

func TestDisableSSO_Success(t *testing.T) {
	var gotBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Contains(t, r.URL.Path, "/resource/res-1")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	err := c.DisableSSO(context.Background(), "res-1")
	require.NoError(t, err)
	assert.Equal(t, false, gotBody["sso"])
	assert.Nil(t, gotBody["skipToIdpId"])
}
