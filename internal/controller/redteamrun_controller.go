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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
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
	// targetName names the target model's Deployment and Service, so the
	// harness can reach the model at http://target:11434.
	targetName = "target"
	// targetPort is the port Ollama serves on.
	targetPort int32 = 11434
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
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create

// Reconcile creates the sandbox namespace for a RedTeamRun and starts the
// target model inside it. The run owns everything it creates, so Kubernetes
// deletes all of it when the run is deleted.
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

	if err := r.createIfMissing(ctx, &run, targetDeployment(&run, nsName)); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.createIfMissing(ctx, &run, targetService(nsName)); err != nil {
		return ctrl.Result{}, err
	}

	if run.Status.Namespace != nsName {
		run.Status.Namespace = nsName
		if err := r.Status().Update(ctx, &run); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

// createIfMissing creates obj with the run as its controller owner, unless it
// already exists. A run's spec can't change, so existing objects never need updating.
func (r *RedTeamRunReconciler) createIfMissing(ctx context.Context, run *redteamv1alpha1.RedTeamRun, obj client.Object) error {
	existing := obj.DeepCopyObject().(client.Object)
	err := r.Get(ctx, client.ObjectKeyFromObject(obj), existing)
	if !apierrors.IsNotFound(err) {
		return err
	}
	if err := controllerutil.SetControllerReference(run, obj, r.Scheme); err != nil {
		return err
	}
	return r.Create(ctx, obj)
}

// sandboxNamespace returns the name of the namespace a run's sandbox lives in.
func sandboxNamespace(run *redteamv1alpha1.RedTeamRun) string {
	return sandboxPrefix + run.Name
}

// targetDeployment runs the target model from the run's image.
func targetDeployment(run *redteamv1alpha1.RedTeamRun, namespace string) *appsv1.Deployment {
	labels := map[string]string{"app": targetName}
	replicas := int32(1)
	automountToken := false
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: targetName, Namespace: namespace, Labels: labels},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					// The model has no reason to talk to the Kubernetes API.
					AutomountServiceAccountToken: &automountToken,
					Containers: []corev1.Container{{
						Name:            targetName,
						Image:           run.Spec.TargetImage,
						ImagePullPolicy: corev1.PullIfNotPresent,
						Ports:           []corev1.ContainerPort{{Name: "http", ContainerPort: targetPort}},
						ReadinessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								HTTPGet: &corev1.HTTPGetAction{Path: "/", Port: intstr.FromString("http")},
							},
						},
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("500m"),
								corev1.ResourceMemory: resource.MustParse("1Gi"),
							},
							Limits: corev1.ResourceList{
								corev1.ResourceMemory: resource.MustParse("2Gi"),
							},
						},
					}},
				},
			},
		},
	}
}

// targetService gives the target model a stable address inside the sandbox.
func targetService(namespace string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: targetName, Namespace: namespace},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": targetName},
			Ports: []corev1.ServicePort{{
				Name:       "http",
				Port:       targetPort,
				TargetPort: intstr.FromString("http"),
			}},
		},
	}
}

// SetupWithManager sets up the controller with the Manager. Owning these types
// means that deleting one of a run's objects triggers a reconcile, which recreates it.
func (r *RedTeamRunReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&redteamv1alpha1.RedTeamRun{}).
		Owns(&corev1.Namespace{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Named("redteamrun").
		Complete(r)
}
