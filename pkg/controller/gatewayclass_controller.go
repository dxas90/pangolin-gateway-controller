package controller

import (
	"context"

	"github.com/dxas90/pangolin-gateway-controller/pkg/config"
	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// GatewayClassReconciler reconciles a GatewayClass object
type GatewayClassReconciler struct {
	client.Client
	Log            logr.Logger
	Scheme         *runtime.Scheme
	ControllerName string
	Config         *config.ControllerConfig
	Recorder       record.EventRecorder
}

// Reconcile handles GatewayClass resources
func (r *GatewayClassReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("gatewayclass", req.NamespacedName)

	// Fetch the GatewayClass
	var gatewayClass gatewayv1.GatewayClass
	if err := r.Get(ctx, req.NamespacedName, &gatewayClass); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Check if this GatewayClass is managed by our controller
	if string(gatewayClass.Spec.ControllerName) != r.ControllerName {
		log.V(1).Info("GatewayClass not managed by this controller", "controllerName", gatewayClass.Spec.ControllerName)
		return ctrl.Result{}, nil
	}

	log.Info("Reconciling GatewayClass")

	// Update status to Accepted
	acceptedCondition := metav1.Condition{
		Type:               string(gatewayv1.GatewayClassConditionStatusAccepted),
		Status:             metav1.ConditionTrue,
		ObservedGeneration: gatewayClass.Generation,
		LastTransitionTime: metav1.Now(),
		Reason:             string(gatewayv1.GatewayClassReasonAccepted),
		Message:            "GatewayClass accepted by Pangolin Gateway Controller",
	}

	// Check if condition already exists and is up to date; append or update in a single pass
	needsUpdate := true
	found := false
	for i, cond := range gatewayClass.Status.Conditions {
		if cond.Type == acceptedCondition.Type {
			found = true
			if cond.Status == acceptedCondition.Status &&
				cond.Reason == acceptedCondition.Reason &&
				cond.ObservedGeneration == acceptedCondition.ObservedGeneration {
				needsUpdate = false
			} else {
				gatewayClass.Status.Conditions[i] = acceptedCondition
			}
			break
		}
	}
	if !found {
		gatewayClass.Status.Conditions = append(gatewayClass.Status.Conditions, acceptedCondition)
	}

	if needsUpdate {
		// Update status
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			current := &gatewayv1.GatewayClass{}
			if err := r.Get(ctx, req.NamespacedName, current); err != nil {
				return err
			}
			current.Status.Conditions = gatewayClass.Status.Conditions
			return r.Status().Update(ctx, current)
		}); err != nil {
			log.Error(err, "Failed to update GatewayClass status")
			return ctrl.Result{}, err
		}

		log.Info("Successfully updated GatewayClass status to Accepted")
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager
func (r *GatewayClassReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&gatewayv1.GatewayClass{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Named("gatewayclass").
		WithOptions(controller.Options{
			MaxConcurrentReconciles: r.Config.MaxConcurrentReconciles,
			RateLimiter:             buildRateLimiter(r.Config),
		}).
		Complete(r)
}
