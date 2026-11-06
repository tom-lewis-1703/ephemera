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
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
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
	// harnessName names the Job that runs garak.
	harnessName = "garak"
	// runTimeoutSeconds stops a garak Job that runs longer than an hour.
	runTimeoutSeconds int64 = 3600
	// waitForTarget polls the target model through its Service until it answers.
	waitForTarget = `import time, urllib.request
while True:
    try:
        urllib.request.urlopen("http://target:11434/", timeout=2)
        break
    except Exception:
        time.sleep(1)
`
)

// RedTeamRunReconciler reconciles a RedTeamRun object
type RedTeamRunReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// HarnessImage is the garak image each run's Job uses.
	HarnessImage string
}

// +kubebuilder:rbac:groups=redteam.ephemera.dev,resources=redteamruns,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=redteam.ephemera.dev,resources=redteamruns/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=redteam.ephemera.dev,resources=redteamruns/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create

// Reconcile moves a RedTeamRun through its lifecycle: set up the sandbox,
// wait for the target model, run garak, record the results on the run and
// delete the sandbox. The run owns everything it creates, so deleting the
// run also deletes anything still left.
func (r *RedTeamRunReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var run redteamv1alpha1.RedTeamRun
	if err := r.Get(ctx, req.NamespacedName, &run); err != nil {
		// A deleted run needs nothing from us, since the owner reference cleans up its namespace.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !run.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	if finished(&run) {
		return ctrl.Result{}, r.deleteSandbox(ctx, &run)
	}

	nsName := sandboxNamespace(&run)
	if err := r.ensureNamespace(ctx, &run, nsName); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.createIfMissing(ctx, &run, targetDeployment(&run, nsName)); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.createIfMissing(ctx, &run, targetService(nsName)); err != nil {
		return ctrl.Result{}, err
	}

	status := run.Status.DeepCopy()
	status.Namespace = nsName
	if status.Phase == "" {
		status.Phase = redteamv1alpha1.RunPending
		status.HarnessImage = r.HarnessImage
	}

	ready, err := r.targetReady(ctx, nsName)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !ready {
		status.Message = "Waiting for the target model to be ready"
		return ctrl.Result{}, r.updateStatus(ctx, &run, status)
	}

	// Garak only starts once the model answers, so its first attacks don't fail.
	if err := r.createIfMissing(ctx, &run, harnessJob(&run, nsName, status.HarnessImage)); err != nil {
		return ctrl.Result{}, err
	}
	var job batchv1.Job
	if err := r.Get(ctx, client.ObjectKey{Namespace: nsName, Name: harnessName}, &job); err != nil {
		// Reads come from a cache, which may not have the Job we just created
		// yet. Its creation triggers another reconcile, so there's no need to retry.
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, r.updateStatus(ctx, &run, status)
		}
		return ctrl.Result{}, err
	}

	switch {
	case jobHasCondition(&job, batchv1.JobComplete):
		message, err := r.harnessMessage(ctx, nsName)
		if err != nil {
			return ctrl.Result{}, err
		}
		results, err := parseResults(message)
		if err != nil {
			finish(status, redteamv1alpha1.RunFailed, err.Error())
		} else {
			status.Results = results
			finish(status, redteamv1alpha1.RunSucceeded, fmt.Sprintf("garak scored %d probe and detector pairs", len(results)))
		}
	case jobHasCondition(&job, batchv1.JobFailed):
		// On failure the container's last log lines become its termination message.
		message, err := r.harnessMessage(ctx, nsName)
		if err != nil {
			return ctrl.Result{}, err
		}
		finish(status, redteamv1alpha1.RunFailed, "garak failed: "+lastLines(message, 10))
	default:
		status.Phase = redteamv1alpha1.RunRunning
		status.Message = "garak is attacking the target model"
	}

	if err := r.updateStatus(ctx, &run, status); err != nil {
		return ctrl.Result{}, err
	}
	if finished(&run) {
		return ctrl.Result{}, r.deleteSandbox(ctx, &run)
	}
	return ctrl.Result{}, nil
}

// ensureNamespace creates the run's sandbox namespace, unless the run already has it.
func (r *RedTeamRunReconciler) ensureNamespace(ctx context.Context, run *redteamv1alpha1.RedTeamRun, name string) error {
	var ns corev1.Namespace
	err := r.Get(ctx, client.ObjectKey{Name: name}, &ns)
	switch {
	case apierrors.IsNotFound(err):
		ns = corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:   name,
				Labels: map[string]string{runLabel: run.Name},
			},
		}
		if err := controllerutil.SetControllerReference(run, &ns, r.Scheme); err != nil {
			return err
		}
		if err := r.Create(ctx, &ns); err != nil {
			return err
		}
		logf.FromContext(ctx).Info("Created sandbox namespace", "namespace", name)
		return nil
	case err != nil:
		return err
	case !metav1.IsControlledBy(&ns, run):
		// Taking over a namespace the run didn't create would get it deleted along with the run.
		return fmt.Errorf("namespace %q already exists and doesn't belong to this run", name)
	}
	return nil
}

// deleteSandbox deletes a finished run's namespace. The results are already
// on the run, so nothing in the sandbox is needed anymore.
func (r *RedTeamRunReconciler) deleteSandbox(ctx context.Context, run *redteamv1alpha1.RedTeamRun) error {
	var ns corev1.Namespace
	err := r.Get(ctx, client.ObjectKey{Name: sandboxNamespace(run)}, &ns)
	if err != nil {
		return client.IgnoreNotFound(err)
	}
	if !metav1.IsControlledBy(&ns, run) || !ns.DeletionTimestamp.IsZero() {
		return nil
	}
	logf.FromContext(ctx).Info("Deleting sandbox namespace", "namespace", ns.Name)
	return client.IgnoreNotFound(r.Delete(ctx, &ns, client.PropagationPolicy(metav1.DeletePropagationBackground)))
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

// targetReady reports whether the target model's Deployment has a ready pod.
func (r *RedTeamRunReconciler) targetReady(ctx context.Context, namespace string) (bool, error) {
	var deploy appsv1.Deployment
	if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: targetName}, &deploy); err != nil {
		// A Deployment that was just created may not be in the cache yet.
		return false, client.IgnoreNotFound(err)
	}
	return deploy.Status.AvailableReplicas > 0, nil
}

// harnessMessage returns the termination message of the garak Job's pod.
func (r *RedTeamRunReconciler) harnessMessage(ctx context.Context, namespace string) (string, error) {
	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(namespace),
		client.MatchingLabels{batchv1.JobNameLabel: harnessName}); err != nil {
		return "", err
	}
	for _, pod := range pods.Items {
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.State.Terminated != nil && cs.State.Terminated.Message != "" {
				return cs.State.Terminated.Message, nil
			}
		}
	}
	return "", nil
}

// updateStatus writes status to the run, unless nothing changed. It patches
// instead of updating, so it doesn't fail when the cached run is out of date.
// Only the operator writes status, so there's nothing to overwrite by accident.
func (r *RedTeamRunReconciler) updateStatus(ctx context.Context, run *redteamv1alpha1.RedTeamRun, status *redteamv1alpha1.RedTeamRunStatus) error {
	if equalStatus(&run.Status, status) {
		return nil
	}
	original := run.DeepCopy()
	run.Status = *status
	return r.Status().Patch(ctx, run, client.MergeFrom(original))
}

// equalStatus compares two statuses by their JSON form.
func equalStatus(a, b *redteamv1alpha1.RedTeamRunStatus) bool {
	aj, errA := json.Marshal(a)
	bj, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(aj) == string(bj)
}

// finished reports whether a run is over, successfully or not.
func finished(run *redteamv1alpha1.RedTeamRun) bool {
	return run.Status.Phase == redteamv1alpha1.RunSucceeded || run.Status.Phase == redteamv1alpha1.RunFailed
}

// finish marks a status as ended with the given phase and message.
func finish(status *redteamv1alpha1.RedTeamRunStatus, phase redteamv1alpha1.RunPhase, message string) {
	now := metav1.Now()
	status.Phase = phase
	status.Message = message
	status.CompletionTime = &now
}

// jobHasCondition reports whether a Job has a condition of the given type set to True.
func jobHasCondition(job *batchv1.Job, condition batchv1.JobConditionType) bool {
	for _, c := range job.Status.Conditions {
		if c.Type == condition && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// harnessSummary is what the harness writes to its termination message once
// garak finishes. It has to stay under Kubernetes' 4096-byte limit.
type harnessSummary struct {
	Results []redteamv1alpha1.ProbeResult `json:"results"`
}

// parseResults reads garak's results out of the harness termination message.
func parseResults(message string) ([]redteamv1alpha1.ProbeResult, error) {
	var summary harnessSummary
	if err := json.Unmarshal([]byte(message), &summary); err != nil {
		return nil, fmt.Errorf("couldn't read garak's results: %w", err)
	}
	if len(summary.Results) == 0 {
		return nil, fmt.Errorf("garak finished without scoring anything")
	}
	return summary.Results, nil
}

// lastLines returns the last n lines of s, so a long log fits in a status message.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
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

// harnessJob runs garak once against the target model.
func harnessJob(run *redteamv1alpha1.RedTeamRun, namespace, image string) *batchv1.Job {
	// A retry would mix two attempts' results, so a failed run stays failed.
	backoffLimit := int32(0)
	timeout := runTimeoutSeconds
	automountToken := false
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: harnessName, Namespace: namespace},
		Spec: batchv1.JobSpec{
			BackoffLimit:          &backoffLimit,
			ActiveDeadlineSeconds: &timeout,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy:                corev1.RestartPolicyNever,
					AutomountServiceAccountToken: &automountToken,
					// A ready pod isn't reachable through its Service right away, because
					// routing to it takes a moment to update. Without this wait, garak's
					// first request can fail to connect.
					InitContainers: []corev1.Container{{
						Name:            "wait-for-target",
						Image:           image,
						ImagePullPolicy: corev1.PullIfNotPresent,
						Command:         []string{"python", "-c", waitForTarget},
					}},
					Containers: []corev1.Container{{
						Name:            harnessName,
						Image:           image,
						ImagePullPolicy: corev1.PullIfNotPresent,
						Args:            harnessArgs(run),
						// Without a summary, the end of garak's log is the next best explanation.
						TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("250m"),
								corev1.ResourceMemory: resource.MustParse("512Mi"),
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

// harnessArgs are the garak arguments for a run.
func harnessArgs(run *redteamv1alpha1.RedTeamRun) []string {
	// The API server defaults generations to 1, but a run built in code may leave it at 0.
	generations := max(run.Spec.Generations, 1)
	return []string{
		"--target_type", "ollama",
		"--target_name", run.Spec.TargetModel,
		"--generator_options", fmt.Sprintf(`{"ollama":{"host":"http://%s:%d"}}`, targetName, targetPort),
		"--probes", strings.Join(run.Spec.Probes, ","),
		"--generations", strconv.Itoa(int(generations)),
	}
}

// SetupWithManager sets up the controller with the Manager. Owning these types
// means a change to any of a run's objects triggers a reconcile, such as the
// target model becoming ready or the garak Job finishing.
func (r *RedTeamRunReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&redteamv1alpha1.RedTeamRun{}).
		Owns(&corev1.Namespace{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&batchv1.Job{}).
		Named("redteamrun").
		Complete(r)
}
