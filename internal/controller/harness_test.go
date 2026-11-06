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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	redteamv1alpha1 "github.com/tom-lewis-1703/ephemera/api/v1alpha1"
)

// envtest stores objects but runs no controllers, so these tests set the
// Deployment's status by hand to stand in for the model becoming ready.
var _ = Describe("RedTeamRun harness", func() {
	ctx := context.Background()
	const harnessImage = "example.com/harness:test"

	createRun := func(name string) *redteamv1alpha1.RedTeamRun {
		run := &redteamv1alpha1.RedTeamRun{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: redteamv1alpha1.RedTeamRunSpec{
				TargetImage: testTargetImage,
				TargetModel: testTargetModel,
				Probes:      []string{"dan.Dan_11_0", "promptinject"},
			},
		}
		Expect(k8sClient.Create(ctx, run)).To(Succeed())
		DeferCleanup(func() {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, run))).To(Succeed())
		})
		return run
	}

	reconcileRun := func(run *redteamv1alpha1.RedTeamRun) {
		r := &RedTeamRunReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), HarnessImage: harnessImage}
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: run.Name}})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(run), run)).To(Succeed())
	}

	It("waits for the target model before starting garak", func() {
		run := createRun("waits-for-target")
		namespace := "redteam-waits-for-target"

		reconcileRun(run)
		Expect(run.Status.Phase).To(Equal(redteamv1alpha1.RunPending))
		Expect(run.Status.HarnessImage).To(Equal(harnessImage))
		var job batchv1.Job
		err := k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "garak"}, &job)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())

		By("marking the target model ready")
		var deploy appsv1.Deployment
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "target"}, &deploy)).To(Succeed())
		deploy.Status.Replicas = 1
		deploy.Status.UpdatedReplicas = 1
		deploy.Status.ReadyReplicas = 1
		deploy.Status.AvailableReplicas = 1
		Expect(k8sClient.Status().Update(ctx, &deploy)).To(Succeed())

		reconcileRun(run)
		Expect(run.Status.Phase).To(Equal(redteamv1alpha1.RunRunning))
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "garak"}, &job)).To(Succeed())
		Expect(metav1.IsControlledBy(&job, run)).To(BeTrue())
		Expect(*job.Spec.BackoffLimit).To(BeZero())
		pod := job.Spec.Template.Spec
		Expect(pod.AutomountServiceAccountToken).To(HaveValue(BeFalse()))
		Expect(pod.InitContainers).To(ConsistOf(HaveField("Name", "wait-for-target")))
		Expect(pod.Containers[0].Image).To(Equal(harnessImage))
		Expect(pod.Containers[0].Args).To(Equal([]string{
			"--target_type", "ollama",
			"--target_name", testTargetModel,
			"--generator_options", `{"ollama":{"host":"http://target:11434"}}`,
			"--probes", "dan.Dan_11_0,promptinject",
			"--generations", "1",
		}))
	})

	It("deletes the sandbox once the run is finished", func() {
		run := createRun("deletes-sandbox")
		reconcileRun(run)

		run.Status.Phase = redteamv1alpha1.RunSucceeded
		Expect(k8sClient.Status().Update(ctx, run)).To(Succeed())
		reconcileRun(run)

		// envtest has no namespace controller, so the namespace stays in
		// Terminating instead of disappearing.
		var ns corev1.Namespace
		Expect(k8sClient.Get(ctx, client.ObjectKey{Name: "redteam-deletes-sandbox"}, &ns)).To(Succeed())
		Expect(ns.DeletionTimestamp).NotTo(BeNil())
	})
})

var _ = Describe("parseResults", func() {
	It("reads the harness summary", func() {
		results, err := parseResults(`{"results":[{"probe":"dan.Dan_11_0","detector":"dan.DAN","passed":0,"total":1}]}`)
		Expect(err).NotTo(HaveOccurred())
		Expect(results).To(Equal([]redteamv1alpha1.ProbeResult{
			{Probe: "dan.Dan_11_0", Detector: "dan.DAN", Passed: 0, Total: 1},
		}))
	})

	It("rejects a message that isn't a summary", func() {
		_, err := parseResults("Traceback (most recent call last):")
		Expect(err).To(HaveOccurred())
	})

	It("rejects a summary with no results", func() {
		_, err := parseResults(`{"results":[]}`)
		Expect(err).To(MatchError(ContainSubstring("without scoring anything")))
	})
})
