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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	redteamv1alpha1 "github.com/tom-lewis-1703/ephemera/api/v1alpha1"
)

var _ = Describe("RedTeamRun target model", func() {
	ctx := context.Background()

	It("starts the target model in the sandbox namespace", func() {
		run := &redteamv1alpha1.RedTeamRun{
			ObjectMeta: metav1.ObjectMeta{Name: "starts-target"},
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

		key := client.ObjectKey{Namespace: "redteam-starts-target", Name: "target"}

		var deploy appsv1.Deployment
		Expect(k8sClient.Get(ctx, key, &deploy)).To(Succeed())
		Expect(metav1.IsControlledBy(&deploy, run)).To(BeTrue())
		pod := deploy.Spec.Template.Spec
		Expect(pod.Containers).To(HaveLen(1))
		Expect(pod.Containers[0].Image).To(Equal(run.Spec.TargetImage))
		Expect(pod.AutomountServiceAccountToken).To(HaveValue(BeFalse()))

		var svc corev1.Service
		Expect(k8sClient.Get(ctx, key, &svc)).To(Succeed())
		Expect(metav1.IsControlledBy(&svc, run)).To(BeTrue())
		Expect(svc.Spec.Ports).To(ConsistOf(HaveField("Port", targetPort)))
	})
})
