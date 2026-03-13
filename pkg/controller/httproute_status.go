package controller

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

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
		// Preserve LastTransitionTime if status hasn't changed for this parent
		for _, ps := range current.Status.Parents {
			if ps.ParentRef.Name == parentRef.Name {
				for _, existing := range ps.Conditions {
					if existing.Type == condition.Type && existing.Status == condition.Status {
						condition.LastTransitionTime = existing.LastTransitionTime
						break
					}
				}
				break
			}
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
