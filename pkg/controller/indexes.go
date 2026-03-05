package controller

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const (
	// GatewayClassNameField is the field index for Gateway.Spec.GatewayClassName
	GatewayClassNameField = ".spec.gatewayClassName"

	// HTTPRouteParentRefsField is the field index for HTTPRoute parent references
	HTTPRouteParentRefsField = ".spec.parentRefs"

	// GRPCRouteParentRefsField is the field index for GRPCRoute parent references
	GRPCRouteParentRefsField = ".spec.parentRefs"
)

// SetupIndexes sets up field indexes for efficient querying.
// This converts O(n) full cache scans into O(1) lookups for frequently-filtered fields.
//
// Example usage after setup:
//
//	var gateways gatewayv1.GatewayList
//	r.List(ctx, &gateways, client.MatchingFields{
//	    GatewayClassNameField: "pangolin",
//	})
func SetupIndexes(mgr manager.Manager) error {
	// Index Gateways by GatewayClassName for fast filtering
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(),
		&gatewayv1.Gateway{},
		GatewayClassNameField,
		func(obj client.Object) []string {
			gateway := obj.(*gatewayv1.Gateway)
			return []string{string(gateway.Spec.GatewayClassName)}
		},
	); err != nil {
		return err
	}

	// Index HTTPRoutes by parent Gateway references
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(),
		&gatewayv1.HTTPRoute{},
		HTTPRouteParentRefsField,
		func(obj client.Object) []string {
			route := obj.(*gatewayv1.HTTPRoute)
			var parentNames []string
			for _, parentRef := range route.Spec.ParentRefs {
				// Index by Gateway name (assuming same namespace)
				if parentRef.Kind != nil && *parentRef.Kind == "Gateway" {
					parentNames = append(parentNames, string(parentRef.Name))
				}
			}
			return parentNames
		},
	); err != nil {
		return err
	}

	// Index GRPCRoutes by parent Gateway references
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(),
		&gatewayv1.GRPCRoute{},
		GRPCRouteParentRefsField,
		func(obj client.Object) []string {
			route := obj.(*gatewayv1.GRPCRoute)
			var parentNames []string
			for _, parentRef := range route.Spec.ParentRefs {
				if parentRef.Kind != nil && *parentRef.Kind == "Gateway" {
					parentNames = append(parentNames, string(parentRef.Name))
				}
			}
			return parentNames
		},
	); err != nil {
		return err
	}

	return nil
}
