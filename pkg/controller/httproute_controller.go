package controller

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dxas90/pangolin-gateway-controller/pkg/config"
	"github.com/dxas90/pangolin-gateway-controller/pkg/pangolin"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
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

// HTTPRouteReconciler reconciles HTTPRoute resources
type HTTPRouteReconciler struct {
	client.Client
	Log            logr.Logger
	Scheme         *runtime.Scheme
	PangolinClient pangolin.ClientInterface
	Config         *config.ControllerConfig
	Recorder       record.EventRecorder
}

// Reconcile implements the reconciliation logic for HTTPRoute resources
func (r *HTTPRouteReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("httproute", req.NamespacedName)
	log.Info("Reconciling HTTPRoute")

	ctx, cancel := context.WithTimeout(ctx, ReconcileTimeout)
	defer cancel()

	// Fetch the HTTPRoute instance
	route := &gatewayv1.HTTPRoute{}
	if err := r.Get(ctx, req.NamespacedName, route); err != nil {
		if errors.IsNotFound(err) {
			log.V(1).Info("HTTPRoute resource not found, likely deleted")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get HTTPRoute")
		return ctrl.Result{}, err
	}

	// Handle deletion
	if !route.ObjectMeta.DeletionTimestamp.IsZero() {
		return r.handleDelete(ctx, route, log)
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(route, FinalizerName) {
		original := route.DeepCopy()
		controllerutil.AddFinalizer(route, FinalizerName)
		if err := r.Patch(ctx, route, client.MergeFrom(original)); err != nil {
			log.Error(err, "Failed to add finalizer")
			return ctrl.Result{}, err
		}
	}

	// Reconcile the HTTPRoute
	return r.reconcileHTTPRoute(ctx, route, log)
}

// handleDelete handles the deletion of an HTTPRoute resource
// Only deletes targets belonging to this HTTPRoute, not the whole resource
// Deletes the resource only if no targets remain
func (r *HTTPRouteReconciler) handleDelete(ctx context.Context, route *gatewayv1.HTTPRoute, log logr.Logger) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(route, FinalizerName) {
		return ctrl.Result{}, nil
	}

	log.Info("Deleting HTTPRoute targets from Pangolin")

	// Delete only the targets belonging to this HTTPRoute
	// Resources are shared across HTTPRoutes with the same hostname
	for _, hostname := range route.Spec.Hostnames {
		// Check for context cancellation in hostname loop
		select {
		case <-ctx.Done():
			return ctrl.Result{}, ctx.Err()
		default:
		}

		// Find the resource for this hostname by querying the API
		resourceID, err := r.findExistingResourceBySubdomainAPI(ctx, string(hostname), log)
		if err != nil || resourceID == "" {
			log.Info("Resource not found, skipping target deletion", "hostname", hostname)
			continue
		}

		// Get all targets for this resource
		existingTargets, err := r.PangolinClient.ListTargetsRaw(ctx, resourceID)
		if err != nil {
			log.Error(err, "Failed to list targets for deletion", "resourceID", resourceID)
			continue
		}

		// Delete targets that belong to this HTTPRoute (match by route namespace/name in target metadata)
		var remainingTargets []map[string]interface{}
		for _, target := range existingTargets {
			// Check for context cancellation in target deletion loop
			select {
			case <-ctx.Done():
				return ctrl.Result{}, ctx.Err()
			default:
			}

			targetIDFloat, ok := target["targetId"].(float64)
			if !ok {
				remainingTargets = append(remainingTargets, target)
				continue
			}
			targetID := fmt.Sprintf("%.0f", targetIDFloat)

			// Check if this target belongs to this HTTPRoute using ownership labels
			shouldDelete := false
			if labels, ok := target["labels"].(map[string]interface{}); ok {
				if routeName, ok := labels["gateway.pangolin.net/httproute-name"].(string); ok {
					if routeNamespace, ok := labels["gateway.pangolin.net/httproute-namespace"].(string); ok {
						if routeName == route.Name && routeNamespace == route.Namespace {
							shouldDelete = true
						}
					}
				}
			} else {
				// Legacy targets without labels - delete all for safety (backwards compatibility)
				shouldDelete = true
				log.V(1).Info("Target has no ownership labels, deleting (legacy target)", "targetId", targetID)
			}

			if !shouldDelete {
				log.V(1).Info("Skipping target from different HTTPRoute", "targetId", targetID)
				remainingTargets = append(remainingTargets, target)
				continue
			}

			log.Info("Deleting target", "targetId", targetID, "resourceID", resourceID, "hostname", hostname)
			if err := r.PangolinClient.DeleteTarget(ctx, targetID); err != nil {
				log.Error(err, "Failed to delete target", "targetId", targetID)
				remainingTargets = append(remainingTargets, target)
			}
		}

		deletedCount := len(existingTargets) - len(remainingTargets)
		log.Info("Deleted targets for hostname", "hostname", hostname, "resourceID", resourceID, "count", deletedCount)

		// Use the remaining targets slice to determine whether to delete the resource
		if len(remainingTargets) == 0 {
			// No targets left, delete the resource
			log.Info("No targets remain, deleting resource", "resourceID", resourceID, "hostname", hostname)
			if err := r.PangolinClient.DeleteResource(ctx, resourceID); err != nil {
				log.Error(err, "Failed to delete resource", "resourceID", resourceID)
			} else {
				log.Info("Successfully deleted resource", "resourceID", resourceID)
			}
		}
	}

	// Remove finalizer
	original := route.DeepCopy()
	controllerutil.RemoveFinalizer(route, FinalizerName)
	if err := r.Patch(ctx, route, client.MergeFrom(original)); err != nil {
		log.Error(err, "Failed to remove finalizer")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// reconcileHTTPRoute reconciles the HTTPRoute with Pangolin
func (r *HTTPRouteReconciler) reconcileHTTPRoute(ctx context.Context, route *gatewayv1.HTTPRoute, log logr.Logger) (ctrl.Result, error) {
	// Get the parent Gateway
	if len(route.Spec.ParentRefs) == 0 {
		log.Info("No parent gateway references found")
		r.updateRouteStatus(ctx, route, false, "NoParentGateway", "No parent gateway references configured")
		return ctrl.Result{}, nil
	}

	parentRef := route.Spec.ParentRefs[0]
	gatewayName := string(parentRef.Name)
	gatewayNamespace := route.Namespace
	if parentRef.Namespace != nil {
		gatewayNamespace = string(*parentRef.Namespace)
	}

	// Fetch the Gateway
	gateway := &gatewayv1.Gateway{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      gatewayName,
		Namespace: gatewayNamespace,
	}, gateway); err != nil {
		log.Error(err, "Failed to get parent Gateway")
		r.updateRouteStatus(ctx, route, false, "ParentGatewayNotFound", "Parent gateway not found")
		return ctrl.Result{RequeueAfter: 30 * time.Second}, err
	}

	// Get the site ID from the Gateway
	siteIDStr := gateway.Labels[SiteIDLabel]
	if siteIDStr == "" {
		log.Info("Gateway does not have site ID yet")
		r.updateRouteStatus(ctx, route, false, "GatewayNotReady", "Parent gateway is not ready")
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	// Process one resource per unique hostname (shared across HTTPRoutes)
	if len(route.Spec.Hostnames) == 0 {
		log.Info("No hostnames specified, cannot create resources")
		r.updateRouteStatus(ctx, route, false, "NoHostnames", "HTTPRoute has no hostnames configured")
		return ctrl.Result{}, nil
	}

	// Cache domain list once for the entire reconciliation
	domains, err := r.PangolinClient.ListDomains(ctx)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to list domains: %w", err)
	}

	// Cache resource list once and build a name→resourceID map
	allResources, err := r.PangolinClient.ListResources(ctx)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to list resources: %w", err)
	}
	resourcesByName := make(map[string]string)
	for _, res := range allResources {
		if name, ok := res["name"].(string); ok {
			if id, ok := res["resourceId"].(string); ok {
				resourcesByName[name] = id
			}
		}
	}

	// Process each hostname
	processedHostnames := 0
	for _, hostname := range route.Spec.Hostnames {
		// Extract subdomain for resource creation (still needed for Pangolin API)
		subdomain, _, err := extractSubdomainFromDomains(string(hostname), domains)
		if err != nil {
			// Skip hostnames that don't match any Pangolin domain
			log.Info("Skipping hostname that doesn't match any Pangolin domain", "hostname", hostname)
			continue
		}

		// Check if resource already exists for this hostname (search by resource name)
		resourceID := r.findExistingResourceBySubdomainFromMap(string(hostname), resourcesByName)

		if resourceID == "" {
			// Create new resource using hostname as name (e.g., "test.dev0ps.me")
			resourceName := string(hostname)
			newResourceID, err := r.createPangolinResourceForHostname(ctx, route, gateway, string(hostname), resourceName, domains, log)
			if err != nil {
				log.Error(err, "Failed to create Pangolin resource", "hostname", hostname)
				r.updateRouteStatus(ctx, route, false, "ResourceCreationFailed", err.Error())
				r.Recorder.Eventf(route, corev1.EventTypeWarning, "ResourceCreationFailed", "Failed to create Pangolin resource for hostname %s: %s", hostname, sanitizeEventMessage(err))
				return ctrl.Result{RequeueAfter: 30 * time.Second}, err
			}
			resourceID = newResourceID
			log.Info("Created Pangolin resource", "resourceID", resourceID, "hostname", hostname, "resourceName", resourceName)
			r.Recorder.Eventf(route, corev1.EventTypeNormal, "ResourceCreated", "Created Pangolin resource %s for hostname %s", resourceID, hostname)
		} else {
			log.V(1).Info("Using existing Pangolin resource", "resourceID", resourceID, "hostname", hostname, "subdomain", subdomain)
		}

		// Create or update targets for this HTTPRoute's rules under the shared resource
		if err := r.reconcileTargets(ctx, route, resourceID, siteIDStr, gateway, log); err != nil {
			log.Error(err, "Failed to reconcile targets", "resourceID", resourceID, "hostname", hostname)
			r.updateRouteStatus(ctx, route, false, "TargetError", err.Error())
			r.Recorder.Eventf(route, corev1.EventTypeWarning, "TargetError", "Failed to reconcile targets for hostname %s: %s", hostname, sanitizeEventMessage(err))
			return ctrl.Result{RequeueAfter: 30 * time.Second}, err
		}
		processedHostnames++
	}

	if processedHostnames == 0 {
		log.Info("No hostnames matched Pangolin domains, HTTPRoute has no valid hostnames")
		r.updateRouteStatus(ctx, route, false, "NoMatchingDomains", "None of the hostnames match Pangolin domains")
		return ctrl.Result{}, nil
	}

	// Update HTTPRoute status
	r.updateRouteStatus(ctx, route, true, "Accepted", "HTTPRoute is configured in Pangolin")
	r.Recorder.Eventf(route, corev1.EventTypeNormal, "Accepted", "HTTPRoute configured in Pangolin (%d hostname(s))", processedHostnames)

	log.Info("Successfully reconciled HTTPRoute", "processedHostnames", processedHostnames, "totalHostnames", len(route.Spec.Hostnames))
	// Requeue after 5 minutes to periodically verify resource still exists
	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

// reconcileTargets creates or updates backend targets in Pangolin
// Each HTTPRoute rule becomes a target with routing rules (path, priority, rewrite)
func (r *HTTPRouteReconciler) reconcileTargets(ctx context.Context, route *gatewayv1.HTTPRoute, resourceID, siteID string, gateway *gatewayv1.Gateway, log logr.Logger) error {
	// Parse siteID once
	siteIDInt, err := strconv.Atoi(siteID)
	if err != nil {
		return fmt.Errorf("invalid site ID %s: %w", siteID, err)
	}

	// Check for context cancellation before starting expensive operations
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	// Get existing targets from Pangolin
	existingTargets, err := r.PangolinClient.ListTargetsRaw(ctx, resourceID)
	if err != nil {
		log.V(1).Info("Failed to list existing targets, will attempt to create anyway", "error", err.Error())
		existingTargets = []map[string]interface{}{} // Continue with empty list
	}

	// Track which existing targets are still needed (to identify orphans)
	matchedTargetIDs := make(map[string]bool)

	// Create targets for each rule (not just each backend)
	for ruleIdx, rule := range route.Spec.Rules {
		// Check for context cancellation in loop
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if len(rule.BackendRefs) == 0 {
			log.V(1).Info("Skipping rule with no backends", "ruleIndex", ruleIdx)
			continue
		}

		// Extract path and matching from rule (shared across all backends in the rule)
		path := "/"
		pathMatchType := "prefix"
		if len(rule.Matches) > 0 && rule.Matches[0].Path != nil {
			path = *rule.Matches[0].Path.Value
			if rule.Matches[0].Path.Type != nil {
				switch *rule.Matches[0].Path.Type {
				case gatewayv1.PathMatchExact:
					pathMatchType = "exact"
				case gatewayv1.PathMatchPathPrefix:
					pathMatchType = "prefix"
				case gatewayv1.PathMatchRegularExpression:
					pathMatchType = "regex"
				}
			}
		}

		// Create one Pangolin target per backend (supports weighted traffic splitting)
		for backendIdx, backendRef := range rule.BackendRefs {
			// Check for context cancellation in backend loop
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}

			serviceName := string(backendRef.Name)
			serviceNamespace := route.Namespace
			if backendRef.Namespace != nil {
				serviceNamespace = string(*backendRef.Namespace)
			}

			// Get the Service to find its ClusterIP
			svc := &corev1.Service{}
			if err := r.Get(ctx, types.NamespacedName{
				Name:      serviceName,
				Namespace: serviceNamespace,
			}, svc); err != nil {
				return fmt.Errorf("failed to get service %s: %w", serviceName, err)
			}

			clusterIP := svc.Spec.ClusterIP
			if clusterIP == "" || clusterIP == "None" {
				return fmt.Errorf("service %s has no ClusterIP", serviceName)
			}

			port := int(*backendRef.Port)

			// Priority: weight overrides rule+backend order
			// Example: rule 0, backend 0 → 110; rule 0, backend 1 → 120; rule 1 → 210...
			priority := (ruleIdx+1)*100 + (backendIdx+1)*10
			if backendRef.Weight != nil {
				priority = int(*backendRef.Weight)
			}

			// Check if target already exists with same ip:port:siteId:path
			// Also check for drift in configuration (pathMatchType, priority, method)
			targetExists := false
			needsUpdate := false
			var existingTargetID string

			for _, target := range existingTargets {
				if fmt.Sprintf("%v", target["ip"]) == clusterIP &&
					numericToString(target["port"]) == strconv.Itoa(port) &&
					numericToString(target["siteId"]) == strconv.Itoa(siteIDInt) &&
					fmt.Sprintf("%v", target["path"]) == path {
					targetExists = true
					existingTargetID = fmt.Sprintf("%v", target["targetId"])

					// Mark this target as matched (still needed)
					matchedTargetIDs[existingTargetID] = true

					// Check for configuration drift
					if fmt.Sprintf("%v", target["pathMatchType"]) != pathMatchType {
						needsUpdate = true
						log.Info("Target drift detected: pathMatchType changed", "targetId", existingTargetID, "old", target["pathMatchType"], "new", pathMatchType)
					}
					if numericToString(target["priority"]) != strconv.Itoa(priority) {
						needsUpdate = true
						log.Info("Target drift detected: priority changed", "targetId", existingTargetID, "old", target["priority"], "new", priority)
					}
					if fmt.Sprintf("%v", target["method"]) != "http" {
						needsUpdate = true
						log.Info("Target drift detected: method changed", "targetId", existingTargetID, "old", target["method"], "new", "http")
					}

					if !needsUpdate {
						log.V(1).Info("Target already exists with correct configuration, skipping", "ip", clusterIP, "port", port, "path", path, "targetId", existingTargetID)
					}
					break
				}
			}

			if targetExists && !needsUpdate {
				continue
			}

			// If target exists but has drifted, delete it first
			if targetExists && needsUpdate {
				select {
				case <-ctx.Done():
					return ctx.Err()
				default:
				}

				log.V(1).Info("Deleting drifted target", "targetId", existingTargetID)
				if err := r.PangolinClient.DeleteTarget(ctx, existingTargetID); err != nil {
					log.Error(err, "Failed to delete drifted target", "targetId", existingTargetID)
					// Continue to try recreating anyway
				}
			}

			// Check for context cancellation before create operation
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}

			// Create target via Integration API: PUT /resource/{resourceId}/target
			targetData := map[string]interface{}{
				"siteId":        siteIDInt,
				"ip":            clusterIP,
				"port":          port,
				"method":        "http",
				"enabled":       true,
				"path":          path,
				"pathMatchType": pathMatchType,
				"priority":      priority,
			}

			createdTarget, err := r.PangolinClient.CreateTargetRaw(ctx, resourceID, targetData)
			if err != nil {
				return fmt.Errorf("failed to create target for rule %d backend %d: %w", ruleIdx, backendIdx, err)
			}

			targetID := fmt.Sprintf("%v", createdTarget["targetId"])
			log.Info("Created target", "targetID", targetID, "ip", clusterIP, "port", port, "path", path,
				"pathMatchType", pathMatchType, "priority", priority, "service", serviceName,
				"backend", fmt.Sprintf("%d/%d", backendIdx+1, len(rule.BackendRefs)))
			r.Recorder.Eventf(route, corev1.EventTypeNormal, "TargetCreated",
				"Created target %s (rule %d, backend %s:%d, path=%s)", targetID, ruleIdx, clusterIP, port, path)

			// Mark newly created target as matched
			matchedTargetIDs[targetID] = true
		}
	}

	// Build a set of paths from the current HTTPRoute's rules
	// Only delete orphaned targets that match paths defined in this HTTPRoute
	routePaths := make(map[string]bool)
	for _, rule := range route.Spec.Rules {
		rulePath := "/"
		if len(rule.Matches) > 0 && rule.Matches[0].Path != nil {
			rulePath = *rule.Matches[0].Path.Value
		}
		routePaths[rulePath] = true
	}

	// Clean up orphaned targets that belong to this HTTPRoute's paths
	// Only delete targets whose path matches one of this HTTPRoute's rule paths
	for _, existingTarget := range existingTargets {
		// Check for context cancellation in cleanup loop
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		existingTargetID := fmt.Sprintf("%v", existingTarget["targetId"])
		targetPath := fmt.Sprintf("%v", existingTarget["path"])

		// Check if this target's path matches any of this HTTPRoute's rule paths
		if !routePaths[targetPath] {
			// This target's path doesn't match any of our rules - it belongs to another HTTPRoute
			log.V(1).Info("Skipping target with non-matching path (from different HTTPRoute)", "targetId", existingTargetID, "targetPath", targetPath)
			continue
		}

		// If target wasn't matched, it's orphaned and should be deleted
		if !matchedTargetIDs[existingTargetID] {
			orphanedIP := fmt.Sprintf("%v", existingTarget["ip"])
			orphanedPort := fmt.Sprintf("%v", existingTarget["port"])
			orphanedPath := fmt.Sprintf("%v", existingTarget["path"])

			log.Info("Deleting orphaned target", "targetId", existingTargetID, "ip", orphanedIP, "port", orphanedPort, "path", orphanedPath)
			if err := r.PangolinClient.DeleteTarget(ctx, existingTargetID); err != nil {
				log.Error(err, "Failed to delete orphaned target", "targetId", existingTargetID)
				// Continue with other orphaned targets
			} else {
				log.Info("Successfully deleted orphaned target", "targetId", existingTargetID)
			}
		}
	}

	return nil
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
				resourceID := fmt.Sprintf("%v", resource["resourceId"])
				log.V(1).Info("Found existing resource with matching hostname", "resourceID", resourceID, "hostname", hostname)
				return resourceID, nil
			}
		}
	}

	return "", nil
}

// createPangolinResourceForHostname creates a new resource in Pangolin for a specific hostname
func (r *HTTPRouteReconciler) createPangolinResourceForHostname(ctx context.Context, route *gatewayv1.HTTPRoute, gateway *gatewayv1.Gateway, hostname, resourceName string, domains []map[string]interface{}, log logr.Logger) (string, error) {
	// Extract subdomain by removing domain suffix using the cached domain list
	subdomain, domainID, err := extractSubdomainFromDomains(hostname, domains)
	if err != nil {
		// Fall back to using hostname as subdomain with default domainID
		subdomain = hostname
		domainID = "domain1"
		log.V(1).Info("No domain match found, using hostname as subdomain", "hostname", hostname)
	} else {
		log.V(1).Info("Matched hostname to domain", "hostname", hostname, "subdomain", subdomain, "domainID", domainID)
	}

	// Create resource via Integration API
	// Endpoint: PUT /org/{orgId}/resource
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

	// Extract resource ID from response
	resourceID := fmt.Sprintf("%v", respData["resourceId"])
	if resourceID == "" {
		return "", fmt.Errorf("no resourceId in response")
	}

	// Extract headers from HTTPRoute filters (RequestHeaderModifier)
	headers := r.extractHeadersFromRoute(route, log)

	// Update resource with headers if any are defined
	// Headers are shared across all HTTPRoutes using this hostname
	if len(headers) > 0 {
		log.V(1).Info("Updating resource with headers", "resourceID", resourceID, "headerCount", len(headers))
		updateData := map[string]interface{}{
			"stickySession": false,
			"ssl":           true,
			"headers":       headers,
		}
		if err := r.PangolinClient.UpdateResource(ctx, resourceID, updateData); err != nil {
			log.Error(err, "Failed to update resource with headers", "resourceID", resourceID)
			// Don't fail resource creation if header update fails
		}
	} else {
		log.V(1).Info("No headers to add for this resource", "resourceID", resourceID)
	}

	// Check if user wants to disable SSO via annotation
	disableSSO := false // Default: SSO remains enabled
	if val, ok := route.Annotations["gateway.pangolin.net/disable-sso"]; ok && val == "true" {
		disableSSO = true
	}

	if disableSSO {
		log.V(1).Info("Disabling SSO for resource", "resourceID", resourceID)
		if err := r.PangolinClient.DisableSSO(ctx, resourceID); err != nil {
			log.Error(err, "Failed to disable SSO", "resourceID", resourceID)
			// Don't fail resource creation if SSO disable fails
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

	// Check each rule for RequestHeaderModifier filters
	for _, rule := range route.Spec.Rules {
		for _, filter := range rule.Filters {
			if filter.Type == gatewayv1.HTTPRouteFilterRequestHeaderModifier {
				if filter.RequestHeaderModifier != nil {
					// Add headers from the filter
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

// updateRouteStatus updates the HTTPRoute status with conflict retry.
func (r *HTTPRouteReconciler) updateRouteStatus(ctx context.Context, route *gatewayv1.HTTPRoute, accepted bool, reason, message string) {
	if len(route.Spec.ParentRefs) == 0 {
		return
	}
	key := client.ObjectKeyFromObject(route)
	parentRef := route.Spec.ParentRefs[0]
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current := &gatewayv1.HTTPRoute{}
		if err := r.Get(ctx, key, current); err != nil {
			return err
		}
		condition := metav1.Condition{
			Type:               string(gatewayv1.RouteConditionAccepted),
			Status:             metav1.ConditionFalse,
			Reason:             reason,
			Message:            message,
			ObservedGeneration: current.Generation,
			LastTransitionTime: metav1.Now(),
		}
		if accepted {
			condition.Status = metav1.ConditionTrue
		}
		newParent := gatewayv1.RouteParentStatus{
			ParentRef:      parentRef,
			ControllerName: gatewayv1.GatewayController(ControllerName),
			Conditions:     []metav1.Condition{condition},
		}
		found := false
		for i, ps := range current.Status.Parents {
			if ps.ParentRef.Name == newParent.ParentRef.Name {
				current.Status.Parents[i] = newParent
				found = true
				break
			}
		}
		if !found {
			current.Status.Parents = append(current.Status.Parents, newParent)
		}
		return r.Status().Update(ctx, current)
	}); err != nil {
		r.Log.Error(err, "Failed to update HTTPRoute status")
	}
}

// SetupWithManager sets up the controller with the Manager
func (r *HTTPRouteReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("httproute").
		For(&gatewayv1.HTTPRoute{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: r.Config.MaxConcurrentReconciles,
			RateLimiter:             buildRateLimiter(r.Config),
		}).
		Complete(r)
}
