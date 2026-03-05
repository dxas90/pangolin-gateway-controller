package controller

import (
	"context"
	"fmt"
	"strconv"
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
		// Find the resource for this hostname (search by resource name)
		resourceID, err := r.findExistingResourceBySubdomain(ctx, string(hostname), log)
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
		targetsDeleted := 0
		for _, target := range existingTargets {
			targetIDFloat, ok := target["targetId"].(float64)
			if !ok {
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
				continue
			}

			log.Info("Deleting target", "targetId", targetID, "resourceID", resourceID, "hostname", hostname)
			if err := r.PangolinClient.DeleteTarget(ctx, targetID); err != nil {
				log.Error(err, "Failed to delete target", "targetId", targetID)
			} else {
				targetsDeleted++
			}
		}

		log.Info("Deleted targets for hostname", "hostname", hostname, "resourceID", resourceID, "count", targetsDeleted)

		// Check if any targets remain
		remainingTargets, err := r.PangolinClient.ListTargetsRaw(ctx, resourceID)
		if err == nil && len(remainingTargets) == 0 {
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

	// Process each hostname
	processedHostnames := 0
	for _, hostname := range route.Spec.Hostnames {
		// Extract subdomain for resource creation (still needed for Pangolin API)
		subdomain, err := r.extractSubdomain(ctx, string(hostname), log)
		if err != nil {
			// Skip hostnames that don't match any Pangolin domain
			log.Info("Skipping hostname that doesn't match any Pangolin domain", "hostname", hostname)
			continue
		}

		// Check if resource already exists for this hostname (search by resource name)
		resourceID, err := r.findExistingResourceBySubdomain(ctx, string(hostname), log)
		if err != nil {
			log.Error(err, "Failed to check for existing resource", "hostname", hostname)
			r.updateRouteStatus(ctx, route, false, "ResourceLookupFailed", err.Error())
			return ctrl.Result{RequeueAfter: 30 * time.Second}, err
		}

		if resourceID == "" {
			// Create new resource using hostname as name (e.g., "test.dev0ps.me")
			resourceName := string(hostname)
			newResourceID, err := r.createPangolinResourceForHostname(ctx, route, gateway, string(hostname), resourceName, log)
			if err != nil {
				log.Error(err, "Failed to create Pangolin resource", "hostname", hostname)
				r.updateRouteStatus(ctx, route, false, "ResourceCreationFailed", err.Error())
				return ctrl.Result{RequeueAfter: 30 * time.Second}, err
			}
			resourceID = newResourceID
			log.Info("Created Pangolin resource", "resourceID", resourceID, "hostname", hostname, "resourceName", resourceName)
		} else {
			log.V(1).Info("Using existing Pangolin resource", "resourceID", resourceID, "hostname", hostname, "subdomain", subdomain)
		}

		// Create or update targets for this HTTPRoute's rules under the shared resource
		if err := r.reconcileTargets(ctx, route, resourceID, siteIDStr, gateway, log); err != nil {
			log.Error(err, "Failed to reconcile targets", "resourceID", resourceID, "hostname", hostname)
			r.updateRouteStatus(ctx, route, false, "TargetError", err.Error())
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
		if len(rule.BackendRefs) == 0 {
			log.V(1).Info("Skipping rule with no backends", "ruleIndex", ruleIdx)
			continue
		}

		// Use first backend for now (TODO: handle multiple backends with weights)
		backendRef := rule.BackendRefs[0]
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

		// Extract path and matching from rule
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

		// Calculate priority from weight (or use rule order if no weight)
		priority := (ruleIdx + 1) * 10 // Default: 10, 20, 30...
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
				fmt.Sprintf("%v", target["port"]) == fmt.Sprintf("%d", port) &&
				fmt.Sprintf("%v", target["siteId"]) == fmt.Sprintf("%d", siteIDInt) &&
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
				if fmt.Sprintf("%v", target["priority"]) != fmt.Sprintf("%d", priority) {
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
			log.V(1).Info("Deleting drifted target", "targetId", existingTargetID)
			if err := r.PangolinClient.DeleteTarget(ctx, existingTargetID); err != nil {
				log.Error(err, "Failed to delete drifted target", "targetId", existingTargetID)
				// Continue to try recreating anyway
			}
		}

		// Create target via Integration API: PUT /resource/{resourceId}/target
		// Include routing rules (path matching, priority, health checks)
		// Note: labels field not supported by Integration API, only used internally by Pangolin UI
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
			return fmt.Errorf("failed to create target for rule %d: %w", ruleIdx, err)
		}

		targetID := fmt.Sprintf("%v", createdTarget["targetId"])
		log.Info("Created target with routing rules", "targetID", targetID, "ip", clusterIP, "port", port, "path", path, "pathMatchType", pathMatchType, "priority", priority, "service", serviceName)

		// Mark newly created target as matched
		matchedTargetIDs[targetID] = true
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

// extractSubdomain extracts the subdomain part from a full hostname by removing the domain suffix
func (r *HTTPRouteReconciler) extractSubdomain(ctx context.Context, hostname string, log logr.Logger) (string, error) {
	// Get organization domains
	domains, err := r.PangolinClient.ListDomains(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to list organization domains: %w", err)
	}

	log.V(1).Info("Checking domains for hostname", "hostname", hostname, "domainCount", len(domains))

	// Match hostname against organization domains to extract subdomain
	for _, domain := range domains {
		domainName, ok := domain["baseDomain"].(string)
		if !ok {
			continue
		}

		log.V(1).Info("Checking domain", "hostname", hostname, "domainName", domainName, "hostnameLen", len(hostname), "domainLen", len(domainName))

		// Check if hostname ends with this domain
		if len(hostname) > len(domainName) && hostname[len(hostname)-len(domainName):] == domainName {
			// Extract subdomain by removing the domain suffix
			// Example: "test.dev0ps.me" with domain "dev0ps.me" -> "test"
			if hostname[len(hostname)-len(domainName)-1] == '.' {
				subdomain := hostname[:len(hostname)-len(domainName)-1]
				log.V(1).Info("Extracted subdomain with dot", "hostname", hostname, "domain", domainName, "subdomain", subdomain)
				return subdomain, nil
			}
			subdomain := hostname[:len(hostname)-len(domainName)]
			log.V(1).Info("Extracted subdomain without dot", "hostname", hostname, "domain", domainName, "subdomain", subdomain)
			return subdomain, nil
		}
	}

	// If no domain matches, return error - hostname doesn't match any Pangolin domain
	log.Info("Hostname does not match any Pangolin domain, skipping", "hostname", hostname, "availableDomains", len(domains))
	return "", fmt.Errorf("hostname %s does not match any Pangolin domain", hostname)
}

// verifyOrRecreateResourceForHostname checks if the resource exists in Pangolin and recreates if deleted
func (r *HTTPRouteReconciler) verifyOrRecreateResourceForHostname(ctx context.Context, route *gatewayv1.HTTPRoute, gateway *gatewayv1.Gateway, resourceID, hostname string, idx int, log logr.Logger) error {
	// Try to get existing targets to verify resource exists
	_, err := r.PangolinClient.ListTargetsRaw(ctx, resourceID)
	if err != nil {
		// Resource likely doesn't exist, recreate it
		log.Info("Resource not found in Pangolin, recreating", "resourceID", resourceID, "hostname", hostname, "index", idx)
		resourceName := fmt.Sprintf("%s-%s-%d", route.Namespace, route.Name, idx)
		newResourceID, createErr := r.createPangolinResourceForHostname(ctx, route, gateway, hostname, resourceName, log)
		if createErr != nil {
			return fmt.Errorf("failed to recreate resource: %w", createErr)
		}

		// Update label with new resource ID
		resourceLabelKey := fmt.Sprintf("%s-%d", ResourceIDLabel, idx)
		route.Labels[resourceLabelKey] = newResourceID
		if updateErr := r.Update(ctx, route); updateErr != nil {
			return fmt.Errorf("failed to update HTTPRoute with new resource ID: %w", updateErr)
		}

		log.Info("Successfully recreated resource", "oldResourceID", resourceID, "newResourceID", newResourceID)
	}

	return nil
}

// findExistingResourceBySubdomain checks if a resource with the given subdomain already exists
// Since we now name resources by full hostname, search by name field
func (r *HTTPRouteReconciler) findExistingResourceBySubdomain(ctx context.Context, hostname string, log logr.Logger) (string, error) {
	// List all existing resources
	resources, err := r.PangolinClient.ListResources(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to list resources: %w", err)
	}

	// Find resource with matching name (we use hostname as resource name)
	for _, resource := range resources {
		if resName, ok := resource["name"].(string); ok {
			if resName == hostname {
				// Found matching resource
				resourceID := fmt.Sprintf("%v", resource["resourceId"])
				log.V(1).Info("Found existing resource with matching hostname", "resourceID", resourceID, "hostname", hostname)
				return resourceID, nil
			}
		}
	}

	// No existing resource found
	return "", nil
}

// createPangolinResourceForHostname creates a new resource in Pangolin for a specific hostname
func (r *HTTPRouteReconciler) createPangolinResourceForHostname(ctx context.Context, route *gatewayv1.HTTPRoute, gateway *gatewayv1.Gateway, hostname, resourceName string, log logr.Logger) (string, error) {
	// Get organization domains
	domains, err := r.PangolinClient.ListDomains(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to list organization domains: %w", err)
	}

	// Extract subdomain by removing domain suffix
	subdomain := hostname
	domainID := "domain1" // Default fallback

	// Match hostname against organization domains to extract subdomain
	for _, domain := range domains {
		domainName, ok := domain["baseDomain"].(string)
		if !ok {
			continue
		}

		// Check if hostname ends with this domain
		if len(hostname) > len(domainName) && hostname[len(hostname)-len(domainName):] == domainName {
			// Extract subdomain by removing the domain suffix
			// Example: "test.dev0ps.me" with domain "dev0ps.me" -> "test"
			if hostname[len(hostname)-len(domainName)-1] == '.' {
				subdomain = hostname[:len(hostname)-len(domainName)-1]
			} else {
				subdomain = hostname[:len(hostname)-len(domainName)]
			}

			// Get domainID (could be "domain1", "domain2", etc.)
			if id, ok := domain["domainId"].(string); ok {
				domainID = id
			}
			log.V(1).Info("Matched hostname to domain", "hostname", hostname, "domain", domainName, "subdomain", subdomain, "domainID", domainID)
			break
		}
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
						log.Info("Found header to add", "name", header.Name, "value", header.Value)
					}
				}
			}
		}
	}

	return headers
}

// reconcileTargets creates or updates backend targets in Pangolin

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
