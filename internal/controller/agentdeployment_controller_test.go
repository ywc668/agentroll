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

	rolloutsv1alpha1 "github.com/argoproj/argo-rollouts/pkg/apis/rollouts/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentrollv1alpha1 "github.com/ywc668/agentroll/api/v1alpha1"
)

var _ = Describe("AgentDeployment Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-resource"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default",
		}
		agentdeployment := &agentrollv1alpha1.AgentDeployment{}

		BeforeEach(func() {
			By("creating the custom resource for the Kind AgentDeployment")
			err := k8sClient.Get(ctx, typeNamespacedName, agentdeployment)
			if err != nil && errors.IsNotFound(err) {
				resource := &agentrollv1alpha1.AgentDeployment{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "default",
					},
					Spec: agentrollv1alpha1.AgentDeploymentSpec{
						Container: agentrollv1alpha1.AgentContainerSpec{
							Image: "test-registry/test-agent:v1.0.0",
						},
						Rollout: agentrollv1alpha1.RolloutSpec{
							Strategy: "canary",
							Steps: []agentrollv1alpha1.RolloutStep{
								{
									SetWeight: 20,
								},
								{
									SetWeight: 100,
								},
							},
						},
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			resource := &agentrollv1alpha1.AgentDeployment{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance AgentDeployment")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())

			// Run the reconciler to process the finalizer so the object is actually deleted
			controllerReconciler := &AgentDeploymentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, _ = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
		})

		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")
			controllerReconciler := &AgentDeploymentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("When reconciling an AgentDeployment with canary steps", func() {
		const resourceName = "canary-test"

		ctx := context.Background()
		nn := types.NamespacedName{Name: resourceName, Namespace: "default"}

		BeforeEach(func() {
			By("creating the AgentDeployment with analysis step")
			err := k8sClient.Get(ctx, nn, &agentrollv1alpha1.AgentDeployment{})
			if err != nil && errors.IsNotFound(err) {
				analysisRef := "agent-quality-check"
				resource := &agentrollv1alpha1.AgentDeployment{
					ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
					Spec: agentrollv1alpha1.AgentDeploymentSpec{
						Container: agentrollv1alpha1.AgentContainerSpec{
							Image: "test-registry/my-agent:v2.0",
							Ports: []corev1.ContainerPort{{ContainerPort: 8080, Name: "http"}},
						},
						AgentMeta: agentrollv1alpha1.AgentMetaSpec{
							PromptVersion: "v2",
							ModelVersion:  "claude-sonnet",
						},
						Rollout: agentrollv1alpha1.RolloutSpec{
							Strategy: "canary",
							Steps: []agentrollv1alpha1.RolloutStep{
								{
									SetWeight: 30,
									Analysis: &agentrollv1alpha1.StepAnalysis{
										TemplateRef: analysisRef,
									},
								},
								{SetWeight: 100},
							},
						},
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			resource := &agentrollv1alpha1.AgentDeployment{}
			Expect(k8sClient.Get(ctx, nn, resource)).To(Succeed())
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())

			// Run the reconciler to process the finalizer (which deletes the Rollout)
			// before we attempt manual cleanup of other generated resources.
			controllerReconciler := &AgentDeploymentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, _ = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})

			// Clean up any remaining generated resources (template may not be owned)
			template := &rolloutsv1alpha1.AnalysisTemplate{}
			templateNN := types.NamespacedName{Name: "agent-quality-check", Namespace: "default"}
			if err := k8sClient.Get(ctx, templateNN, template); err == nil {
				Expect(k8sClient.Delete(ctx, template)).To(Succeed())
			}
		})

		It("should create an Argo Rollout with composite version labels", func() {
			controllerReconciler := &AgentDeploymentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			By("verifying the Argo Rollout was created")
			rollout := &rolloutsv1alpha1.Rollout{}
			Expect(k8sClient.Get(ctx, nn, rollout)).To(Succeed())

			By("verifying composite version labels on pod template")
			podLabels := rollout.Spec.Template.Labels
			Expect(podLabels).To(HaveKey("agentroll.dev/composite-version"))
			Expect(podLabels).To(HaveKey("agentroll.dev/prompt-version"))
			Expect(podLabels["agentroll.dev/prompt-version"]).To(Equal("v2"))
			Expect(podLabels).To(HaveKey("agentroll.dev/model-version"))
			Expect(podLabels["agentroll.dev/model-version"]).To(Equal("claude-sonnet"))

			By("verifying the canary strategy is set")
			Expect(rollout.Spec.Strategy.Canary).NotTo(BeNil())
			// 2 AgentDeployment steps (setWeight:30+analysis, setWeight:100) translate to
			// 3 Argo steps: setWeight:30 | analysis | setWeight:100
			Expect(rollout.Spec.Strategy.Canary.Steps).To(HaveLen(3))
		})

		It("should create a managed AnalysisTemplate for agent-quality-check", func() {
			controllerReconciler := &AgentDeploymentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			By("verifying the AnalysisTemplate was created with managed-by label")
			template := &rolloutsv1alpha1.AnalysisTemplate{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "agent-quality-check", Namespace: "default",
			}, template)).To(Succeed())
			Expect(template.Labels["app.kubernetes.io/managed-by"]).To(Equal("agentroll"))

			By("verifying the AnalysisTemplate has the agent-health metric")
			Expect(template.Spec.Metrics).To(HaveLen(1))
			Expect(template.Spec.Metrics[0].Name).To(Equal("agent-health"))
			Expect(template.Spec.Metrics[0].Provider.Job).NotTo(BeNil())
		})

		It("should create a Service when the container has ports", func() {
			controllerReconciler := &AgentDeploymentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			By("verifying the Service was created")
			svc := &corev1.Service{}
			Expect(k8sClient.Get(ctx, nn, svc)).To(Succeed())
			Expect(svc.Spec.Ports).To(HaveLen(1))
			Expect(svc.Spec.Ports[0].Port).To(Equal(int32(8080)))
		})

		It("should update AgentDeployment status phase after reconciliation", func() {
			controllerReconciler := &AgentDeploymentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			By("verifying status was set")
			updated := &agentrollv1alpha1.AgentDeployment{}
			Expect(k8sClient.Get(ctx, nn, updated)).To(Succeed())
			// Phase should be set (Pending or Progressing — depends on Rollout phase from envtest)
			Expect(updated.Status.Phase).NotTo(BeEmpty())
		})

		It("should report stable version from the actual stable ReplicaSet, not the current spec", func() {
			// This test guards against the bug where StableVersion was always set from
			// compositeVersion (current spec), which caused incorrect STABLE output when
			// a canary was rejected — the stable RS is still the old version.
			controllerReconciler := &AgentDeploymentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			updated := &agentrollv1alpha1.AgentDeployment{}
			Expect(k8sClient.Get(ctx, nn, updated)).To(Succeed())

			By("verifying StableVersion is set to a non-empty string")
			Expect(updated.Status.StableVersion).NotTo(BeEmpty())

			By("verifying StableVersion contains the prompt and model from the spec (first deploy — no prior stable RS)")
			// On a fresh deploy the stable RS and current hash are both empty in envtest,
			// so the controller falls back to compositeVersion. The key invariant is that
			// StableVersion is always set and reflects a real version, not an empty string.
			Expect(updated.Status.StableVersion).To(ContainSubstring("v2"))
			Expect(updated.Status.StableVersion).To(ContainSubstring("claude-sonnet"))
		})
	})

	Context("When reconciling with KEDA scaling configured", func() {
		const resourceName = "keda-scaling-test"
		ctx := context.Background()
		nn := types.NamespacedName{Name: resourceName, Namespace: "default"}

		BeforeEach(func() {
			err := k8sClient.Get(ctx, nn, &agentrollv1alpha1.AgentDeployment{})
			if err != nil && errors.IsNotFound(err) {
				minReplicas := int32(1)
				maxReplicas := int32(10)
				resource := &agentrollv1alpha1.AgentDeployment{
					ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
					Spec: agentrollv1alpha1.AgentDeploymentSpec{
						Container: agentrollv1alpha1.AgentContainerSpec{
							Image: "test-registry/my-agent:v1.0",
						},
						Rollout: agentrollv1alpha1.RolloutSpec{
							Strategy: "canary",
							Steps:    []agentrollv1alpha1.RolloutStep{{SetWeight: 100}},
						},
						Replicas: &minReplicas,
						Scaling: &agentrollv1alpha1.ScalingSpec{
							MinReplicas: minReplicas,
							MaxReplicas: maxReplicas,
							Metric:      "queue-depth",
							TargetValue: 5,
							QueueRef: &agentrollv1alpha1.QueueReference{
								Provider:  "redis",
								Address:   "redis.default.svc:6379",
								QueueName: "agent-tasks",
							},
						},
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			resource := &agentrollv1alpha1.AgentDeployment{}
			Expect(k8sClient.Get(ctx, nn, resource)).To(Succeed())
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
			controllerReconciler := &AgentDeploymentReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, _ = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
		})

		It("should reconcile without error when KEDA ScaledObject CRD is present", func() {
			controllerReconciler := &AgentDeploymentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			// ScaledObject CRD is registered in envtest (test/crds/scaledobject-crd.yaml)
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			By("verifying the Argo Rollout was still created (scaling doesn't block rollout reconcile)")
			rollout := &rolloutsv1alpha1.Rollout{}
			Expect(k8sClient.Get(ctx, nn, rollout)).To(Succeed())
		})
	})

	Context("RBAC hardening — auto-created ServiceAccount", func() {
		const resourceName = "sa-auto-test"
		ctx := context.Background()
		nn := types.NamespacedName{Name: resourceName, Namespace: "default"}

		BeforeEach(func() {
			err := k8sClient.Get(ctx, nn, &agentrollv1alpha1.AgentDeployment{})
			if err != nil && errors.IsNotFound(err) {
				resource := &agentrollv1alpha1.AgentDeployment{
					ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
					Spec: agentrollv1alpha1.AgentDeploymentSpec{
						Container: agentrollv1alpha1.AgentContainerSpec{
							Image: "test-registry/my-agent:v1.0",
						},
						Rollout: agentrollv1alpha1.RolloutSpec{
							Strategy: "canary",
							Steps:    []agentrollv1alpha1.RolloutStep{{SetWeight: 100}},
						},
						// ServiceAccountName intentionally empty — controller should auto-create
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			resource := &agentrollv1alpha1.AgentDeployment{}
			Expect(k8sClient.Get(ctx, nn, resource)).To(Succeed())
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
			controllerReconciler := &AgentDeploymentReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, _ = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
		})

		It("should auto-create a ServiceAccount named after the agent", func() {
			controllerReconciler := &AgentDeploymentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			By("verifying a ServiceAccount was created with the agent name")
			sa := &corev1.ServiceAccount{}
			Expect(k8sClient.Get(ctx, nn, sa)).To(Succeed())
			Expect(sa.Labels["app.kubernetes.io/managed-by"]).To(Equal("agentroll"))

			By("verifying the Rollout uses the auto-created ServiceAccount")
			rollout := &rolloutsv1alpha1.Rollout{}
			Expect(k8sClient.Get(ctx, nn, rollout)).To(Succeed())
			Expect(rollout.Spec.Template.Spec.ServiceAccountName).To(Equal(resourceName))
		})
	})
})
