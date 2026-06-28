package controllers

import (
	"context"

	openbaov1alpha1 "github.com/guardian-intelligence/guardian/src/services/secrets/openbao/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type MountReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func (r *MountReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	return reconcileScaffold(ctx, r.Client, req, &openbaov1alpha1.OpenBaoMount{})
}

func (r *MountReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&openbaov1alpha1.OpenBaoMount{}).
		Complete(r)
}
