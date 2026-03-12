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

// --- UpdateResource ---

func TestUpdateResource_Success(t *testing.T) {
	var gotBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Contains(t, r.URL.Path, "/resource/res-42")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	err := c.UpdateResource(context.Background(), "res-42", map[string]interface{}{
		"ssl": true, "stickySession": false,
	})
	require.NoError(t, err)
	assert.Equal(t, true, gotBody["ssl"])
}

func TestUpdateResource_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"server error"}`))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	err := c.UpdateResource(context.Background(), "res-42", map[string]interface{}{"ssl": true})
	require.Error(t, err)
	assert.True(t, IsPangolinAPIError(err))
}

// --- CreateTarget (typed) ---

func TestCreateTarget_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPut, r.Method)
		assert.Contains(t, r.URL.Path, "/resource/res-1/target")
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"data": map[string]interface{}{
				"id":         "tgt-100",
				"resourceId": "res-1",
				"hostname":   "test.example.com",
				"port":       float64(8080),
				"protocol":   "http",
			},
		})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	target, err := c.CreateTarget(context.Background(), "res-1", &Target{
		Hostname: "test.example.com", Port: 8080, Protocol: "http",
	})
	require.NoError(t, err)
	assert.Equal(t, "tgt-100", target.ID)
	assert.Equal(t, 8080, target.Port)
}

func TestCreateTarget_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad request"}`))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	_, err := c.CreateTarget(context.Background(), "res-1", &Target{})
	require.Error(t, err)
}

func TestCreateTarget_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`not json`))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	_, err := c.CreateTarget(context.Background(), "res-1", &Target{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unmarshal")
}

// --- ListTargets (typed) ---

func TestListTargets_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Contains(t, r.URL.Path, "/resource/res-1/targets")
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"data": map[string]interface{}{
				"targets": []map[string]interface{}{
					{"id": "t1", "hostname": "a.example.com", "port": float64(80)},
					{"id": "t2", "hostname": "b.example.com", "port": float64(443)},
				},
			},
		})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	targets, err := c.ListTargets(context.Background(), "res-1")
	require.NoError(t, err)
	assert.Len(t, targets, 2)
	assert.Equal(t, "t1", targets[0].ID)
}

func TestListTargets_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"forbidden"}`))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	_, err := c.ListTargets(context.Background(), "res-1")
	require.Error(t, err)
}

func TestListTargets_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{broken`))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	_, err := c.ListTargets(context.Background(), "res-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unmarshal")
}

// --- CreateRule ---

func TestCreateRule_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPut, r.Method)
		assert.Contains(t, r.URL.Path, "/resource/res-1/rule")
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"data": map[string]interface{}{
				"id":         "rule-1",
				"resourceId": "res-1",
				"priority":   float64(100),
			},
		})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	rule, err := c.CreateRule(context.Background(), "res-1", &Rule{
		Priority: 100,
		Conditions: []RuleCondition{
			{Type: "path", Operator: "prefix", Value: "/api"},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "rule-1", rule.ID)
}

func TestCreateRule_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"conflict"}`))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	_, err := c.CreateRule(context.Background(), "res-1", &Rule{})
	require.Error(t, err)
}

func TestCreateRule_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`not-json`))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	_, err := c.CreateRule(context.Background(), "res-1", &Rule{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unmarshal")
}

// --- ListRules ---

func TestListRules_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Contains(t, r.URL.Path, "/resource/res-1/rules")
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"data": map[string]interface{}{
				"rules": []map[string]interface{}{
					{"id": "rule-1", "priority": float64(100)},
				},
			},
		})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	rules, err := c.ListRules(context.Background(), "res-1")
	require.NoError(t, err)
	assert.Len(t, rules, 1)
	assert.Equal(t, "rule-1", rules[0].ID)
}

func TestListRules_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not found"}`))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	_, err := c.ListRules(context.Background(), "res-1")
	require.Error(t, err)
}

func TestListRules_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{bad`))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	_, err := c.ListRules(context.Background(), "res-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unmarshal")
}

// --- DeleteRule ---

func TestDeleteRule_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodDelete, r.Method)
		assert.Contains(t, r.URL.Path, "/resource/res-1/rule/rule-1")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	err := c.DeleteRule(context.Background(), "res-1", "rule-1")
	require.NoError(t, err)
}

func TestDeleteRule_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not found"}`))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	err := c.DeleteRule(context.Background(), "res-1", "rule-99")
	require.Error(t, err)
}

// --- SetResourceRoles ---

func TestSetResourceRoles_Success(t *testing.T) {
	var gotBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Contains(t, r.URL.Path, "/resource/res-1/roles")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	err := c.SetResourceRoles(context.Background(), "res-1", []string{"role-a", "role-b"})
	require.NoError(t, err)
	roleIDs, ok := gotBody["roleIds"].([]interface{})
	require.True(t, ok)
	assert.Len(t, roleIDs, 2)
}

func TestSetResourceRoles_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"server error"}`))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	err := c.SetResourceRoles(context.Background(), "res-1", []string{"role-a"})
	require.Error(t, err)
}

// --- GetSite ---

func TestGetSite_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Contains(t, r.URL.Path, "/site/s-1")
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"siteId": 1, "name": "my-site", "online": true,
		})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	site, err := c.GetSite(context.Background(), "s-1")
	require.NoError(t, err)
	assert.Equal(t, "my-site", site.Name)
	assert.True(t, site.Online)
}

func TestGetSite_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not found"}`))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	_, err := c.GetSite(context.Background(), "s-99")
	require.Error(t, err)
}

func TestGetSite_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{broken`))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	_, err := c.GetSite(context.Background(), "s-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unmarshal")
}

// --- GetServerVersion ---

func TestGetServerVersion_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Contains(t, r.URL.Path, "/api/v1/auth/newt/get-token")
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"success": true,
			"data": map[string]interface{}{
				"token":         "jwt-token",
				"serverVersion": "1.16.2",
			},
		})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	version, err := c.GetServerVersion(context.Background(), srv.URL, "newt-id", "newt-secret")
	require.NoError(t, err)
	assert.Equal(t, "1.16.2", version)
}

func TestGetServerVersion_AuthRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"success": false,
			"message": "invalid credentials",
		})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	_, err := c.GetServerVersion(context.Background(), srv.URL, "bad-id", "bad-secret")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rejected")
}

func TestGetServerVersion_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`server error`))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	_, err := c.GetServerVersion(context.Background(), srv.URL, "id", "secret")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "status 500")
}

func TestGetServerVersion_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`not-json`))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	_, err := c.GetServerVersion(context.Background(), srv.URL, "id", "secret")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode")
}

func TestGetServerVersion_ConnectionError(t *testing.T) {
	c := NewClient("key", "org")
	_, err := c.GetServerVersion(context.Background(), "http://127.0.0.1:1", "id", "secret")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reach")
}

// --- ListSites pagination ---

func TestListSites_Pagination(t *testing.T) {
	page := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page++
		if page == 1 {
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"data": map[string]interface{}{
					"sites": []map[string]interface{}{
						{"siteId": 1, "name": "site-1"},
						{"siteId": 2, "name": "site-2"},
					},
					"pagination": map[string]interface{}{
						"total":    float64(3),
						"pageSize": float64(2),
						"page":     float64(1),
					},
				},
			})
		} else {
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"data": map[string]interface{}{
					"sites": []map[string]interface{}{
						{"siteId": 3, "name": "site-3"},
					},
					"pagination": map[string]interface{}{
						"total":    float64(3),
						"pageSize": float64(2),
						"page":     float64(2),
					},
				},
			})
		}
	}))
	defer srv.Close()

	c := newTestClient(srv)
	sites, err := c.ListSites(context.Background())
	require.NoError(t, err)
	assert.Len(t, sites, 3)
	assert.Equal(t, 2, page) // Two pages fetched
}

func TestListSites_EmptyPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"data": map[string]interface{}{
				"sites":      []map[string]interface{}{},
				"pagination": map[string]interface{}{"total": float64(0)},
			},
		})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	sites, err := c.ListSites(context.Background())
	require.NoError(t, err)
	assert.Len(t, sites, 0)
}

func TestListSites_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"fail"}`))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	_, err := c.ListSites(context.Background())
	require.Error(t, err)
}

func TestListSites_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{bad`))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	_, err := c.ListSites(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unmarshal")
}

// --- ListSites legacy pagination ---

func TestListSites_LegacyPagination(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"data": map[string]interface{}{
				"sites": []map[string]interface{}{
					{"siteId": 1, "name": "site-1"},
				},
				"pagination": map[string]interface{}{
					"total": float64(1),
					"limit": float64(1000),
				},
			},
		})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	sites, err := c.ListSites(context.Background())
	require.NoError(t, err)
	assert.Len(t, sites, 1)
}

// --- ListResources pagination ---

func TestListResources_Pagination(t *testing.T) {
	page := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page++
		if page == 1 {
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"data": map[string]interface{}{
					"resources": []map[string]interface{}{
						{"resourceId": "r1"}, {"resourceId": "r2"},
					},
					"pagination": map[string]interface{}{
						"total": float64(3), "pageSize": float64(2), "page": float64(1),
					},
				},
			})
		} else {
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"data": map[string]interface{}{
					"resources": []map[string]interface{}{
						{"resourceId": "r3"},
					},
					"pagination": map[string]interface{}{
						"total": float64(3), "pageSize": float64(2), "page": float64(2),
					},
				},
			})
		}
	}))
	defer srv.Close()

	c := newTestClient(srv)
	resources, err := c.ListResources(context.Background())
	require.NoError(t, err)
	assert.Len(t, resources, 3)
	assert.Equal(t, 2, page)
}

func TestListResources_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"unavailable"}`))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	_, err := c.ListResources(context.Background())
	require.Error(t, err)
}

func TestListResources_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{bad`))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	_, err := c.ListResources(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unmarshal")
}

// --- CreateSite error paths ---

func TestCreateSite_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"conflict"}`))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	_, err := c.CreateSite(context.Background(), &Site{Name: "test"})
	require.Error(t, err)
}

func TestCreateSite_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`not-json`))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	_, err := c.CreateSite(context.Background(), &Site{Name: "test"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unmarshal")
}

// --- PickSiteDefaults error paths ---

func TestPickSiteDefaults_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"forbidden"}`))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	_, err := c.PickSiteDefaults(context.Background())
	require.Error(t, err)
}

func TestPickSiteDefaults_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{bad`))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	_, err := c.PickSiteDefaults(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unmarshal")
}

// --- CreateResource error paths ---

func TestCreateResource_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad request"}`))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	_, err := c.CreateResource(context.Background(), map[string]interface{}{"name": "test"})
	require.Error(t, err)
}

func TestCreateResource_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`not-json`))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	_, err := c.CreateResource(context.Background(), map[string]interface{}{"name": "test"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unmarshal")
}

// --- CreateTargetRaw error paths ---

func TestCreateTargetRaw_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad"}`))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	_, err := c.CreateTargetRaw(context.Background(), "res-1", map[string]interface{}{})
	require.Error(t, err)
}

func TestCreateTargetRaw_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`not-json`))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	_, err := c.CreateTargetRaw(context.Background(), "res-1", map[string]interface{}{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unmarshal")
}

// --- ListTargetsRaw error paths ---

func TestListTargetsRaw_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"fail"}`))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	_, err := c.ListTargetsRaw(context.Background(), "res-1")
	require.Error(t, err)
}

func TestListTargetsRaw_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{bad`))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	_, err := c.ListTargetsRaw(context.Background(), "res-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unmarshal")
}

// --- ListDomains error paths ---

func TestListDomains_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"unavailable"}`))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	_, err := c.ListDomains(context.Background())
	require.Error(t, err)
}

func TestListDomains_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{bad`))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	_, err := c.ListDomains(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unmarshal")
}

// --- doRequest with nil breaker ---

func TestDoRequest_NilBreaker(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"data": map[string]interface{}{
				"sites":      []map[string]interface{}{},
				"pagination": map[string]interface{}{"total": float64(0)},
			},
		})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	c.Breaker = nil
	sites, err := c.ListSites(context.Background())
	require.NoError(t, err)
	assert.Len(t, sites, 0)
}

func TestDoRequest_NilBreaker_5xxStillErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"fail"}`))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	c.Breaker = nil
	_, err := c.ListSites(context.Background())
	require.Error(t, err)
}

// --- normalizePath ---

func TestNormalizePath(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"/org/home/sites?pageSize=1000&page=1", "/org/home/sites"},
		{"/resource/12345/targets", "/resource/{id}/targets"},
		{"/site/42", "/site/{id}"},
		{"/org/home/resources", "/org/home/resources"},
		{"/resource/abcdef12-3456-7890/target", "/resource/{id}/target"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.expected, normalizePath(tt.input))
		})
	}
}

// --- isIDSegment ---

func TestIsIDSegment(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"12345", true},
		{"0", true},
		{"abcdef01", true},  // hex >= 8 chars
		{"abc", false},      // hex < 8 chars
		{"home", false},     // not numeric/hex
		{"", false},         // empty
		{"abc-def-012", true}, // UUID-like with dashes
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.expected, isIDSegment(tt.input))
		})
	}
}

// --- isHexString ---

func TestIsHexString(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"abcdef0123456789", true},
		{"ABCDEF", true},
		{"abc-def", true},
		{"xyz", false},
		{"abc!def", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.expected, isHexString(tt.input))
		})
	}
}
