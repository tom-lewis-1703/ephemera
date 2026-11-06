/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	redteamv1alpha1 "github.com/tom-lewis-1703/ephemera/api/v1alpha1"
)

const (
	// sandboxPrefix is added to a run's name to get its namespace name.
	sandboxPrefix = "redteam-"
	// runLabel marks a sandbox namespace with the name of the run it belongs to.
	runLabel = "ephemera.dev/redteam-run"
)

// RedTeamRunReconciler reconciles a RedTeamRun object
type RedTeamRunReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=redteam.ephemera.dev,resources=redteamruns,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=redteam.ephemera.dev,resources=redteamruns/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=redteam.ephemera.dev,resources=redteamruns/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;create

// Reconcile creates the sandbox namespace for a RedTeamRun. The run owns the
// namespace, so Kubernetes deletes the namespace when the run is deleted.
func (r *RedTeamRunReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var run redteamv1alpha1.RedTeamRun
	if err := r.Get(ctx, req.NamespacedName, &run); err != nil {
		// A deleted run needs nothing from us, since the owner reference cleans up its namespace.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !run.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	nsName := sandboxNamespace(&run)
	var ns corev1.Namespace
	err := r.Get(ctx, client.ObjectKey{Name: nsName}, &ns)
	switch {
	case apierrors.IsNotFound(err):
		ns = corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:   nsName,
				Labels: map[string]string{runLabel: run.Name},
			},
		}
		if err := controllerutil.SetControllerReference(&run, &ns, r.Scheme); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.Create(ctx, &ns); err != nil {
			return ctrl.Result{}, err
		}
		log.Info("Created sandbox namespace", "namespace", nsName)
	case err != nil:
		return ctrl.Result{}, err
	case !metav1.IsControlledBy(&ns, &run):
		// Taking over a namespace the run didn't create would get it deleted along with the run.
		return ctrl.Result{}, fmt.Errorf("namespace %q already exists and doesn't belong to this run", nsName)
	}

	if run.Status.Namespace != nsName {
		run.Status.Namespace = nsName
		if err := r.Status().Update(ctx, &run); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

// sandboxNamespace returns the name of the namespace a run's sandbox lives in.
func sandboxNamespace(run *redteamv1alpha1.RedTeamRun) string {
	return sandboxPrefix + run.Name
}

// SetupWithManager sets up the controller with the Manager. Owning namespaces
// means a deleted sandbox namespace triggers a reconcile, which recreates it.
func (r *RedTeamRunReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&redteamv1alpha1.RedTeamRun{}).
		Owns(&corev1.Namespace{}).
		Named("redteamrun").
		Complete(r)
}
