package controllers

import (
	"context"
	"fmt"

	"git.cclab-inu.com/b0m313/nimbus/operator/api/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"

	general "github.com/5GSEC/nimbus/Nimbus/controllers/general"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type SecurityIntentReconciler struct {
	client.Client
	Scheme            *runtime.Scheme
	GeneralController *general.GeneralController
}

// NewSecurityIntentReconciler creates a new SecurityIntentReconciler.
func NewSecurityIntentReconciler(client client.Client, scheme *runtime.Scheme) *SecurityIntentReconciler {
	if client == nil {
		fmt.Println("SecurityIntentReconciler: Client is nil")
		return nil
	}

	generalController, err := general.NewGeneralController(client)
	if err != nil {
		fmt.Println("SecurityIntentReconciler: Failed to initialize GeneralController:", err)
		return nil
	}

	return &SecurityIntentReconciler{
		Client:            client,
		Scheme:            scheme,
		GeneralController: generalController,
	}
}

// Reconcile handles the reconciliation of the SecurityIntent resources.
func (r *SecurityIntentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	if r.GeneralController == nil {
		fmt.Println("SecurityIntentReconciler: GeneralController is nil")
		return ctrl.Result{}, fmt.Errorf("GeneralController is not properly initialized")
	}

	intent, err := r.GeneralController.WatcherIntent.Reconcile(ctx, req)
	if err != nil {
		log.Error(err, "Error in WatcherIntent.Reconcile", "Request", req.NamespacedName)
		return ctrl.Result{}, err
	}

	if intent != nil {
		log.Info("SecurityIntent resource found", "Name", req.Name, "Namespace", req.Namespace)
	} else {
		log.Info("SecurityIntent resource not found", "Name", req.Name, "Namespace", req.Namespace)
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the reconciler with the provided manager.
func (r *SecurityIntentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Set up the controller to manage SecurityIntent resources.
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1.SecurityIntent{}).
		Complete(r)
}
