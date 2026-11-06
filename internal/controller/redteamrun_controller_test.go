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
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	redteamv1alpha1 "github.com/tom-lewis-1703/ephemera/api/v1alpha1"
)

// envtest runs an API server but no garbage collector, so these tests check
// owner references rather than the namespace actually being deleted.
var _ = Describe("RedTeamRun Controller", func() {
	ctx := context.Background()

	newRun := func(name string) *redteamv1alpha1.RedTeamRun {
		return &redteamv1alpha1.RedTeamRun{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: redteamv1alpha1.RedTeamRunSpec{
				TargetImage: testTargetImage,
				TargetModel: testTargetModel,
				Probes:      []string{testProbe},
			},
		}
	}

	createRun := func(name string) *redteamv1alpha1.RedTeamRun {
		run := newRun(name)
		Expect(k8sClient.Create(ctx, run)).To(Succeed())
		DeferCleanup(func() {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, run))).To(Succeed())
		})
		return run
	}

	reconcileRun := func(name string) error {
		r := &RedTeamRunReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: name}})
		return err
	}

	It("creates a sandbox namespace owned by the run", func() {
		run := createRun("creates-namespace")
		Expect(reconcileRun(run.Name)).To(Succeed())

		var ns corev1.Namespace
		Expect(k8sClient.Get(ctx, client.ObjectKey{Name: "redteam-creates-namespace"}, &ns)).To(Succeed())
		Expect(ns.Labels).To(HaveKeyWithValue(runLabel, run.Name))
		Expect(metav1.IsControlledBy(&ns, run)).To(BeTrue())

		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(run), run)).To(Succeed())
		Expect(run.Status.Namespace).To(Equal("redteam-creates-namespace"))

		By("reconciling again with nothing changed")
		Expect(reconcileRun(run.Name)).To(Succeed())
	})

	It("refuses to take over a namespace it didn't create", func() {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "redteam-taken"}}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		run := createRun("taken")

		Expect(reconcileRun(run.Name)).To(MatchError(ContainSubstring("doesn't belong to this run")))
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(ns), ns)).To(Succeed())
		Expect(ns.OwnerReferences).To(BeEmpty())
	})

	It("rejects changes to the spec", func() {
		run := createRun("immutable-spec")

		run.Spec.Probes = []string{testProbe, "dan"}
		Expect(k8sClient.Update(ctx, run)).To(MatchError(ContainSubstring("spec is immutable")))
	})

	It("rejects names that can't become a namespace name", func() {
		Expect(k8sClient.Create(ctx, newRun("has.dots"))).To(MatchError(ContainSubstring("valid DNS label")))
		Expect(k8sClient.Create(ctx, newRun(strings.Repeat("a", 56)))).To(MatchError(ContainSubstring("at most 55")))
	})
})
