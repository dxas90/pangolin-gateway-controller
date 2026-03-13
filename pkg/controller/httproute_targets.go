package controller

import (
	"context"
	"fmt"
	"strconv"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// reconcileTargets creates or updates backend targets in Pangolin.
// Each HTTPRoute rule becomes a target with routing rules (path, priority, rewrite).
func (r *HTTPRouteReconciler) reconcileTargets(ctx context.Context, route *gatewayv1.HTTPRoute, resourceID, siteID string, log logr.Logger) error {
	siteIDInt, err := strconv.Atoi(siteID)
	if err != nil {
		return fmt.Errorf("invalid site ID %s: %w", siteID, err)
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	existingTargets, err := r.PangolinClient.ListTargetsRaw(ctx, resourceID)
	if err != nil {
		return fmt.Errorf("failed to list existing targets for resource %s: %w", resourceID, err)
	}

	// Track which existing targets are still needed (to identify orphans)
	matchedTargetIDs := make(map[string]bool)

	for ruleIdx, rule := range route.Spec.Rules {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if len(rule.BackendRefs) == 0 {
			log.V(1).Info("Skipping rule with no backends", "ruleIndex", ruleIdx)
			continue
		}

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

		for backendIdx, backendRef := range rule.BackendRefs {
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

			if backendRef.Port == nil {
				log.Error(nil, "BackendRef missing required Port, skipping backend", "service", serviceName, "ruleIndex", ruleIdx, "backendIndex", backendIdx)
				continue
			}
			port := int(*backendRef.Port)

			// Priority: weight overrides rule+backend order
			priority := (ruleIdx+1)*100 + (backendIdx+1)*10
			if backendRef.Weight != nil {
				priority = int(*backendRef.Weight)
			}

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
					matchedTargetIDs[existingTargetID] = true

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

			if targetExists && needsUpdate {
				select {
				case <-ctx.Done():
					return ctx.Err()
				default:
				}
				log.V(1).Info("Deleting drifted target", "targetId", existingTargetID)
				if err := r.PangolinClient.DeleteTarget(ctx, existingTargetID); err != nil {
					log.Error(err, "Failed to delete drifted target", "targetId", existingTargetID)
				}
			}

			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}

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

			matchedTargetIDs[targetID] = true
		}
	}

	// Build set of paths owned by this HTTPRoute
	routePaths := make(map[string]bool)
	for _, rule := range route.Spec.Rules {
		rulePath := "/"
		if len(rule.Matches) > 0 && rule.Matches[0].Path != nil {
			rulePath = *rule.Matches[0].Path.Value
		}
		routePaths[rulePath] = true
	}

	// Clean up orphaned targets that belong to this HTTPRoute's paths
	for _, existingTarget := range existingTargets {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		existingTargetID := fmt.Sprintf("%v", existingTarget["targetId"])
		targetPath := fmt.Sprintf("%v", existingTarget["path"])

		if !routePaths[targetPath] {
			log.V(1).Info("Skipping target with non-matching path (from different HTTPRoute)", "targetId", existingTargetID, "targetPath", targetPath)
			continue
		}

		if !matchedTargetIDs[existingTargetID] {
			orphanedIP := fmt.Sprintf("%v", existingTarget["ip"])
			orphanedPort := fmt.Sprintf("%v", existingTarget["port"])
			orphanedPath := fmt.Sprintf("%v", existingTarget["path"])

			log.Info("Deleting orphaned target", "targetId", existingTargetID, "ip", orphanedIP, "port", orphanedPort, "path", orphanedPath)
			if err := r.PangolinClient.DeleteTarget(ctx, existingTargetID); err != nil {
				log.Error(err, "Failed to delete orphaned target", "targetId", existingTargetID)
			} else {
				log.Info("Successfully deleted orphaned target", "targetId", existingTargetID)
			}
		}
	}

	return nil
}
