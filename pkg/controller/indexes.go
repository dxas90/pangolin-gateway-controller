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

	return nil
}
