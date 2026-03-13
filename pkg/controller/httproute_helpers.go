package controller

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// extractSubdomainFromDomains finds the subdomain and domain ID for a given hostname
// from the list of Pangolin domains. Uses "baseDomain" field as per Pangolin API.
func extractSubdomainFromDomains(hostname string, domains []map[string]interface{}) (subdomain, domainID string, err error) {
	for _, domain := range domains {
		base, _ := domain["baseDomain"].(string)
		id, _ := domain["domainId"].(string)
		if base == "" || id == "" {
			continue
		}
		if strings.HasSuffix(hostname, "."+base) || hostname == base {
			subdomain = strings.TrimSuffix(hostname, "."+base)
			if subdomain == hostname {
				subdomain = hostname // exact match, use as-is
			}
			return subdomain, id, nil
		}
	}
	return "", "", fmt.Errorf("no matching domain found for hostname %s", hostname)
}

// numericToString converts numeric interface values to string for comparison
func numericToString(v interface{}) string {
	switch n := v.(type) {
	case float64:
		return fmt.Sprintf("%.0f", n)
	case int:
		return strconv.Itoa(n)
	case int64:
		return strconv.FormatInt(n, 10)
	case string:
		return n
	default:
		return fmt.Sprintf("%v", v)
	}
}

// sanitizeEventMessage truncates error messages for Kubernetes events to avoid
// leaking large API response bodies into event records.
func sanitizeEventMessage(err error) string {
	msg := err.Error()
	if len(msg) > 200 {
		return msg[:200] + "..."
	}
	return msg
}

// findExistingResourceBySubdomainFromMap looks up a resource ID from a pre-fetched name map.
// Returns empty string if not found or if resourcesByName is nil.
func (r *HTTPRouteReconciler) findExistingResourceBySubdomainFromMap(hostname string, resourcesByName map[string]string) string {
	if resourcesByName == nil {
		return ""
	}
	return resourcesByName[hostname]
}

// findExistingResourceBySubdomainAPI checks if a resource with the given hostname already exists
// by calling the Pangolin API. Used during deletion when no cached map is available.
func (r *HTTPRouteReconciler) findExistingResourceBySubdomainAPI(ctx context.Context, hostname string, log logr.Logger) (string, error) {
	resources, err := r.PangolinClient.ListResources(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to list resources: %w", err)
	}

	for _, resource := range resources {
		if resName, ok := resource["name"].(string); ok {
			if resName == hostname {
				raw := resource["resourceId"]
				if raw == nil {
					continue
				}
				resourceID := fmt.Sprintf("%v", raw)
				log.V(1).Info("Found existing resource with matching hostname", "resourceID", resourceID, "hostname", hostname)
				return resourceID, nil
			}
		}
	}

	return "", nil
}

// createPangolinResourceForHostname creates a new resource in Pangolin for a specific hostname
func (r *HTTPRouteReconciler) createPangolinResourceForHostname(ctx context.Context, route *gatewayv1.HTTPRoute, hostname, resourceName string, domains []map[string]interface{}, log logr.Logger) (string, error) {
	subdomain, domainID, err := extractSubdomainFromDomains(hostname, domains)
	if err != nil {
		subdomain = hostname
		domainID = "domain1"
		log.V(1).Info("No domain match found, using hostname as subdomain", "hostname", hostname)
	} else {
		log.V(1).Info("Matched hostname to domain", "hostname", hostname, "subdomain", subdomain, "domainID", domainID)
	}

	resourceData := map[string]interface{}{
		"name":          resourceName,
		"subdomain":     subdomain,
		"http":          true,
		"protocol":      "tcp",
		"domainId":      domainID,
		"stickySession": false,
	}

	respData, err := r.PangolinClient.CreateResource(ctx, resourceData)
	if err != nil {
		return "", fmt.Errorf("failed to create resource: %w", err)
	}

	rawID := respData["resourceId"]
	if rawID == nil {
		return "", fmt.Errorf("no resourceId in response")
	}
	resourceID := fmt.Sprintf("%v", rawID)
	if resourceID == "" {
		return "", fmt.Errorf("no resourceId in response")
	}

	headers := r.extractHeadersFromRoute(route, log)
	if len(headers) > 0 {
		log.V(1).Info("Updating resource with headers", "resourceID", resourceID, "headerCount", len(headers))
		updateData := map[string]interface{}{
			"stickySession": false,
			"ssl":           true,
			"headers":       headers,
		}
		if err := r.PangolinClient.UpdateResource(ctx, resourceID, updateData); err != nil {
			log.Error(err, "Failed to update resource with headers", "resourceID", resourceID)
		}
	} else {
		log.V(1).Info("No headers to add for this resource", "resourceID", resourceID)
	}

	disableSSO := false
	if val, ok := route.Annotations["gateway.pangolin.net/disable-sso"]; ok && val == "true" {
		disableSSO = true
	}

	if disableSSO {
		log.V(1).Info("Disabling SSO for resource", "resourceID", resourceID)
		if err := r.PangolinClient.DisableSSO(ctx, resourceID); err != nil {
			log.Error(err, "Failed to disable SSO", "resourceID", resourceID)
		} else {
			log.Info("Successfully disabled SSO", "resourceID", resourceID)
		}
	} else {
		log.V(1).Info("SSO remains enabled (use annotation gateway.pangolin.net/disable-sso=true to disable)", "resourceID", resourceID)
	}

	log.Info("Created Pangolin resource", "resourceID", resourceID, "name", resourceName, "subdomain", subdomain)
	return resourceID, nil
}

// extractHeadersFromRoute extracts headers to add from HTTPRoute filters
func (r *HTTPRouteReconciler) extractHeadersFromRoute(route *gatewayv1.HTTPRoute, log logr.Logger) []map[string]string {
	var headers []map[string]string

	for _, rule := range route.Spec.Rules {
		for _, filter := range rule.Filters {
			if filter.Type == gatewayv1.HTTPRouteFilterRequestHeaderModifier {
				if filter.RequestHeaderModifier != nil {
					for _, header := range filter.RequestHeaderModifier.Add {
						headers = append(headers, map[string]string{
							"name":  string(header.Name),
							"value": header.Value,
						})
						log.V(1).Info("Found header to add", "name", header.Name)
					}
				}
			}
		}
	}

	return headers
}

// verifyOrUpdateResource checks if an existing Pangolin resource config matches
// the desired state and updates it if there is drift. Uses the already-fetched
// allResources slice to avoid an extra API call.
func (r *HTTPRouteReconciler) verifyOrUpdateResource(ctx context.Context, route *gatewayv1.HTTPRoute, resourceID, hostname string, allResources []map[string]interface{}, domains []map[string]interface{}, log logr.Logger) error {
	// Find the resource by ID in the pre-fetched list
	var current map[string]interface{}
	for _, res := range allResources {
		if fmt.Sprintf("%v", res["resourceId"]) == resourceID {
			current = res
			break
		}
	}

	if current == nil {
		// Resource disappeared — will be recreated on next reconciliation
		log.Info("Resource no longer found during drift check", "resourceID", resourceID, "hostname", hostname)
		r.Recorder.Eventf(route, corev1.EventTypeWarning, "DriftDetected", "Pangolin resource %s for hostname %s disappeared", resourceID, hostname)
		return nil
	}

	// Check headers drift
	desiredHeaders := r.extractHeadersFromRoute(route, log)
	currentHeadersRaw := current["headers"]
	hasHeaderDrift := len(desiredHeaders) > 0 && currentHeadersRaw == nil

	// Check SSO drift: annotation says disable but SSO is still enabled
	disableSSO := route.Annotations["gateway.pangolin.net/disable-sso"] == "true"
	currentSSO, _ := current["sso"].(bool)
	hasSSODrift := disableSSO && currentSSO

	if !hasHeaderDrift && !hasSSODrift {
		log.V(1).Info("No drift detected on resource config", "resourceID", resourceID, "hostname", hostname)
		return nil
	}

	log.Info("Drift detected on resource config, updating", "resourceID", resourceID, "hostname", hostname, "headerDrift", hasHeaderDrift, "ssoDrift", hasSSODrift)
	r.Recorder.Eventf(route, corev1.EventTypeWarning, "DriftDetected", "Config drift detected on Pangolin resource %s for hostname %s", resourceID, hostname)

	if hasHeaderDrift {
		updateData := map[string]interface{}{
			"stickySession": false,
			"ssl":           true,
			"headers":       desiredHeaders,
		}
		if err := r.PangolinClient.UpdateResource(ctx, resourceID, updateData); err != nil {
			log.Error(err, "Failed to correct header drift", "resourceID", resourceID)
		} else {
			log.Info("Corrected header drift", "resourceID", resourceID)
		}
	}

	if hasSSODrift {
		if err := r.PangolinClient.DisableSSO(ctx, resourceID); err != nil {
			log.Error(err, "Failed to correct SSO drift", "resourceID", resourceID)
		} else {
			log.Info("Corrected SSO drift", "resourceID", resourceID)
		}
	}

	return nil
}
