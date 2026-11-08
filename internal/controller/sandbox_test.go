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
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	redteamv1alpha1 "github.com/tom-lewis-1703/ephemera/api/v1alpha1"
)

// envtest doesn't enforce network policies or run pods, so these tests check
// that the lockdown is requested. hack/escape-test.sh checks it holds on a
// real cluster.
var _ = Describe("RedTeamRun sandbox lockdown", func() {
	ctx := context.Background()

	It("locks down the sandbox before anything runs in it", func() {
		run := &redteamv1alpha1.RedTeamRun{
			ObjectMeta: metav1.ObjectMeta{Name: "locked-down"},
			Spec: redteamv1alpha1.RedTeamRunSpec{
				TargetImage: testTargetImage,
				TargetModel: testTargetModel,
				Probes:      []string{testProbe},
			},
		}
		Expect(k8sClient.Create(ctx, run)).To(Succeed())
		DeferCleanup(func() {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, run))).To(Succeed())
		})

		r := &RedTeamRunReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: run.Name}})
		Expect(err).NotTo(HaveOccurred())
		namespace := "redteam-locked-down"

		By("enforcing the restricted Pod Security level")
		var ns corev1.Namespace
		Expect(k8sClient.Get(ctx, client.ObjectKey{Name: namespace}, &ns)).To(Succeed())
		Expect(ns.Labels).To(HaveKeyWithValue("pod-security.kubernetes.io/enforce", "restricted"))

		By("denying all traffic by default")
		var policies networkingv1.NetworkPolicyList
		Expect(k8sClient.List(ctx, &policies, client.InNamespace(namespace))).To(Succeed())
		Expect(policies.Items).To(ConsistOf(
			HaveField("Name", "default-deny"),
			HaveField("Name", "harness-egress"),
			HaveField("Name", "target-ingress"),
		))
		for _, policy := range policies.Items {
			Expect(metav1.IsControlledBy(&policy, run)).To(BeTrue())
			if policy.Name == "default-deny" {
				Expect(policy.Spec.PodSelector.MatchLabels).To(BeEmpty())
				Expect(policy.Spec.PolicyTypes).To(ConsistOf(networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress))
				Expect(policy.Spec.Ingress).To(BeEmpty())
				Expect(policy.Spec.Egress).To(BeEmpty())
			}
		}

		By("running the target model as a non-root user with no extra privileges")
		var deploy appsv1.Deployment
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: targetName}, &deploy)).To(Succeed())
		pod := deploy.Spec.Template.Spec
		Expect(pod.SecurityContext.RunAsNonRoot).To(HaveValue(BeTrue()))
		Expect(pod.SecurityContext.SeccompProfile.Type).To(Equal(corev1.SeccompProfileTypeRuntimeDefault))
		container := pod.Containers[0].SecurityContext
		Expect(container.AllowPrivilegeEscalation).To(HaveValue(BeFalse()))
		Expect(container.Capabilities.Drop).To(ConsistOf(corev1.Capability("ALL")))
	})
})
