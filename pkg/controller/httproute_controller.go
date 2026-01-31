package controller

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/dxas90/pangolin-gateway-controller/pkg/pangolin"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// HTTPRouteReconciler reconciles HTTPRoute resources
type HTTPRouteReconciler struct {
	client.Client
	Log            logr.Logger
	Scheme         *runtime.Scheme
	PangolinClient *pangolin.Client
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
			log.Info("HTTPRoute resource not found, likely deleted")
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
		controllerutil.AddFinalizer(route, FinalizerName)
		if err := r.Update(ctx, route); err != nil {
			log.Error(err, "Failed to add finalizer")
			return ctrl.Result{}, err
		}
	}

	// Reconcile the HTTPRoute
	return r.reconcileHTTPRoute(ctx, route, log)
}

// handleDelete handles the deletion of an HTTPRoute resource
func (r *HTTPRouteReconciler) handleDelete(ctx context.Context, route *gatewayv1.HTTPRoute, log logr.Logger) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(route, FinalizerName) {
		return ctrl.Result{}, nil
	}

	log.Info("Deleting HTTPRoute from Pangolin")

	// Delete the entire resource from Pangolin (this also deletes all targets and rules)
	resourceID := route.Labels[ResourceIDLabel]
	if resourceID != "" {
		log.Info("Deleting Pangolin resource", "resourceID", resourceID)
		if err := r.PangolinClient.DeleteResource(ctx, resourceID); err != nil {
			log.Error(err, "Failed to delete resource from Pangolin", "resourceID", resourceID)
			// Continue with finalizer removal even if deletion fails
			// (resource might already be deleted)
		} else {
			log.Info("Successfully deleted Pangolin resource", "resourceID", resourceID)
		}
	} else {
		log.Info("No resource ID found in labels, skipping Pangolin cleanup")
	}

	// Remove finalizer
	controllerutil.RemoveFinalizer(route, FinalizerName)
	if err := r.Update(ctx, route); err != nil {
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

	// Get or create resource ID
	resourceID := route.Labels[ResourceIDLabel]
	if resourceID == "" {
		// Check if resource already exists with the same name/subdomain
		existingResourceID, err := r.findExistingResource(ctx, route, log)
		if err != nil {
			log.Error(err, "Failed to check for existing resource")
			r.updateRouteStatus(ctx, route, false, "ResourceLookupFailed", err.Error())
			return ctrl.Result{RequeueAfter: 30 * time.Second}, err
		}

		if existingResourceID != "" {
			// Resource already exists, use it
			resourceID = existingResourceID
			log.Info("Found existing Pangolin resource", "resourceID", resourceID)
		} else {
			// Create new Pangolin resource for this HTTPRoute
			newResourceID, err := r.createPangolinResource(ctx, route, gateway, log)
			if err != nil {
				log.Error(err, "Failed to create Pangolin resource")
				r.updateRouteStatus(ctx, route, false, "ResourceCreationFailed", err.Error())
				return ctrl.Result{RequeueAfter: 30 * time.Second}, err
			}
			resourceID = newResourceID
			log.Info("Created Pangolin resource", "resourceID", resourceID)
		}

		// Update route labels with resource ID
		if route.Labels == nil {
			route.Labels = make(map[string]string)
		}
		route.Labels[ResourceIDLabel] = resourceID
		if err := r.Update(ctx, route); err != nil {
			log.Error(err, "Failed to update HTTPRoute with resource ID")
			return ctrl.Result{}, err
		}
	} else {
		// Verify resource still exists in Pangolin, recreate if deleted
		if err := r.verifyOrRecreateResource(ctx, route, gateway, resourceID, log); err != nil {
			log.Error(err, "Failed to verify/recreate resource")
			r.updateRouteStatus(ctx, route, false, "ResourceVerificationFailed", err.Error())
			return ctrl.Result{RequeueAfter: 30 * time.Second}, err
		}
	}

	// Create or update targets for backends
	if err := r.reconcileTargets(ctx, route, resourceID, siteIDStr, gateway, log); err != nil {
		log.Error(err, "Failed to reconcile targets")
		r.updateRouteStatus(ctx, route, false, "TargetError", err.Error())
		return ctrl.Result{RequeueAfter: 30 * time.Second}, err
	}

	// Update HTTPRoute status
	r.updateRouteStatus(ctx, route, true, "Accepted", "HTTPRoute is configured in Pangolin")

	log.Info("Successfully reconciled HTTPRoute", "resourceID", resourceID)
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
		log.Error(err, "Failed to list existing targets, will attempt to create anyway")
		existingTargets = []map[string]interface{}{} // Continue with empty list
	}

	// Track which existing targets are still needed (to identify orphans)
	matchedTargetIDs := make(map[string]bool)

	// Create targets for each rule (not just each backend)
	for ruleIdx, rule := range route.Spec.Rules {
		if len(rule.BackendRefs) == 0 {
			log.Info("Skipping rule with no backends", "ruleIndex", ruleIdx)
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
					log.Info("Target already exists with correct configuration, skipping", "ip", clusterIP, "port", port, "path", path, "targetId", existingTargetID)
				}
				break
			}
		}

		if targetExists && !needsUpdate {
			continue
		}

		// If target exists but has drifted, delete it first
		if targetExists && needsUpdate {
			log.Info("Deleting drifted target", "targetId", existingTargetID)
			if err := r.PangolinClient.DeleteTarget(ctx, existingTargetID); err != nil {
				log.Error(err, "Failed to delete drifted target", "targetId", existingTargetID)
				// Continue to try recreating anyway
			}
		}

		// Create target via Integration API: PUT /resource/{resourceId}/target
		// Include routing rules (path matching, priority, health checks)
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

	// Clean up orphaned targets that are no longer in the HTTPRoute spec
	// These are targets that exist in Pangolin but weren't matched/recreated above
	for _, existingTarget := range existingTargets {
		existingTargetID := fmt.Sprintf("%v", existingTarget["targetId"])

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

// verifyOrRecreateResource checks if the resource exists in Pangolin and recreates if deleted
func (r *HTTPRouteReconciler) verifyOrRecreateResource(ctx context.Context, route *gatewayv1.HTTPRoute, gateway *gatewayv1.Gateway, resourceID string, log logr.Logger) error {
	// Try to get existing targets to verify resource exists
	_, err := r.PangolinClient.ListTargetsRaw(ctx, resourceID)
	if err != nil {
		// Resource likely doesn't exist, recreate it
		log.Info("Resource not found in Pangolin, recreating", "resourceID", resourceID)
		newResourceID, createErr := r.createPangolinResource(ctx, route, gateway, log)
		if createErr != nil {
			return fmt.Errorf("failed to recreate resource: %w", createErr)
		}
		// Update route labels with new resource ID
		if route.Labels == nil {
			route.Labels = make(map[string]string)
		}
		route.Labels[ResourceIDLabel] = newResourceID
		if err := r.Update(ctx, route); err != nil {
			return fmt.Errorf("failed to update HTTPRoute with new resource ID: %w", err)
		}
		log.Info("Recreated resource after deletion", "oldResourceID", resourceID, "newResourceID", newResourceID)
	}
	return nil
}

// findExistingResource checks if a resource with the same subdomain already exists
func (r *HTTPRouteReconciler) findExistingResource(ctx context.Context, route *gatewayv1.HTTPRoute, log logr.Logger) (string, error) {
	// Get organization domains
	domains, err := r.PangolinClient.ListDomains(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to list organization domains: %w", err)
	}

	// Calculate expected subdomain (same logic as createPangolinResource)
	subdomain := fmt.Sprintf("%s-%s", route.Namespace, route.Name)

	if len(route.Spec.Hostnames) > 0 {
		hostname := string(route.Spec.Hostnames[0])

		// Match hostname against organization domains
		for _, domain := range domains {
			domainName, ok := domain["name"].(string)
			if !ok {
				continue
			}

			// Check if hostname ends with this domain
			if len(hostname) > len(domainName) && hostname[len(hostname)-len(domainName):] == domainName {
				// Extract subdomain
				if hostname[len(hostname)-len(domainName)-1] == '.' {
					subdomain = hostname[:len(hostname)-len(domainName)-1]
				} else {
					subdomain = hostname[:len(hostname)-len(domainName)]
				}
				break
			}
		}
	}

	// List all existing resources
	resources, err := r.PangolinClient.ListResources(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to list resources: %w", err)
	}

	// Find resource with matching subdomain
	for _, resource := range resources {
		if resSubdomain, ok := resource["subdomain"].(string); ok {
			if resSubdomain == subdomain {
				// Found matching resource
				resourceID := fmt.Sprintf("%v", resource["resourceId"])
				log.Info("Found existing resource with matching subdomain", "resourceID", resourceID, "subdomain", subdomain)
				return resourceID, nil
			}
		}
	}

	// No existing resource found
	return "", nil
}

// createPangolinResource creates a new resource in Pangolin for the HTTPRoute
func (r *HTTPRouteReconciler) createPangolinResource(ctx context.Context, route *gatewayv1.HTTPRoute, gateway *gatewayv1.Gateway, log logr.Logger) (string, error) {
	// Get organization domains
	domains, err := r.PangolinClient.ListDomains(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to list organization domains: %w", err)
	}

	// Get the first hostname from the HTTPRoute
	subdomain := fmt.Sprintf("%s-%s", route.Namespace, route.Name)
	domainID := "domain1" // Default fallback

	if len(route.Spec.Hostnames) > 0 {
		hostname := string(route.Spec.Hostnames[0])

		// Match hostname against organization domains
		for _, domain := range domains {
			domainName, ok := domain["name"].(string)
			if !ok {
				continue
			}

			// Check if hostname ends with this domain
			if len(hostname) > len(domainName) && hostname[len(hostname)-len(domainName):] == domainName {
				// Extract subdomain by removing the domain suffix
				// Example: "test.example.com" with domain "example.com" -> "test"
				if hostname[len(hostname)-len(domainName)-1] == '.' {
					subdomain = hostname[:len(hostname)-len(domainName)-1]
				} else {
					subdomain = hostname[:len(hostname)-len(domainName)]
				}

				// Get domainID (could be "domain1", "domain2", etc.)
				if id, ok := domain["domainId"].(string); ok {
					domainID = id
				}
				log.Info("Matched hostname to domain", "hostname", hostname, "domain", domainName, "subdomain", subdomain, "domainID", domainID)
				break
			}
		}
	}

	// Create resource via Integration API
	// Endpoint: PUT /org/{orgId}/resource
	resourceData := map[string]interface{}{
		"name":          fmt.Sprintf("%s-%s", route.Namespace, route.Name),
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

	// Check if user wants to disable SSO via annotation
	disableSSO := false // Default: disable SSO
	if val, ok := route.Annotations["gateway.pangolin.net/disable-sso"]; ok && val == "true" {
		disableSSO = true
	}

	if disableSSO {
		// Disable SSO using PATCH /resource/{resourceId} with {"sso":false}
		log.Info("Attempting to disable SSO via PATCH", "resourceID", resourceID)
		patchData := map[string]interface{}{
			"sso":         false,
			"skipToIdpId": 1,
		}
		if err := r.PangolinClient.DisableSSO(ctx, resourceID, patchData); err != nil {
			log.Error(err, "Failed to PATCH SSO disable - trying fallback POST /roles method", "resourceID", resourceID)
		} else {
			log.Info("Successfully disabled SSO via PATCH", "resourceID", resourceID)
		}
	} else {
		log.Info("SSO remains enabled (annotation gateway.pangolin.net/disable-sso=true)", "resourceID", resourceID)
	}

	log.Info("Created Pangolin resource", "resourceID", resourceID, "subdomain", subdomain)
	return resourceID, nil
}

// reconcileTargets creates or updates backend targets in Pangolin

// updateRouteStatus updates the HTTPRoute status
func (r *HTTPRouteReconciler) updateRouteStatus(ctx context.Context, route *gatewayv1.HTTPRoute, accepted bool, reason, message string) {
	condition := metav1.Condition{
		Type:               string(gatewayv1.RouteConditionAccepted),
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: route.Generation,
		LastTransitionTime: metav1.Now(),
	}

	if accepted {
		condition.Status = metav1.ConditionTrue
	}

	// Update parent status
	parentStatus := gatewayv1.RouteParentStatus{
		ParentRef:      route.Spec.ParentRefs[0],
		ControllerName: gatewayv1.GatewayController(ControllerName),
		Conditions:     []metav1.Condition{condition},
	}

	// Update or append parent status
	found := false
	for i, ps := range route.Status.Parents {
		if ps.ParentRef.Name == parentStatus.ParentRef.Name {
			route.Status.Parents[i] = parentStatus
			found = true
			break
		}
	}

	if !found {
		route.Status.Parents = append(route.Status.Parents, parentStatus)
	}

	if err := r.Status().Update(ctx, route); err != nil {
		r.Log.Error(err, "Failed to update HTTPRoute status")
	}
}

// SetupWithManager sets up the controller with the Manager
func (r *HTTPRouteReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&gatewayv1.HTTPRoute{}).
		Complete(r)
}
