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
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	agentrollv1alpha1 "github.com/ywc668/agentroll/api/v1alpha1"
)

// AgentDeploymentReconciler reconciles a AgentDeployment object
type AgentDeploymentReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// RBAC permissions needed by the controller.
// It needs to manage AgentDeployments (our CRD) plus Deployments and Services (what we create).
// +kubebuilder:rbac:groups=agentroll.agentroll.dev,resources=agentdeployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=agentroll.agentroll.dev,resources=agentdeployments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=agentroll.agentroll.dev,resources=agentdeployments/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete

// Reconcile is the main reconciliation loop.
// It watches AgentDeployment resources and ensures the actual cluster state
// (Deployments, Services) matches the desired state declared in the CRD.
//
// This is the heart of the Kubernetes operator pattern:
//
//	User declares: "I want this agent running with these settings"
//	Controller ensures: "OK, I'll create/update the Deployment and Service to match"
func (r *AgentDeploymentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// =========================================================================
	// Step 1: Fetch the AgentDeployment resource
	// =========================================================================
	agentDeploy := &agentrollv1alpha1.AgentDeployment{}
	if err := r.Get(ctx, req.NamespacedName, agentDeploy); err != nil {
		if errors.IsNotFound(err) {
			// Resource was deleted — nothing to do.
			// Owned resources (Deployment, Service) will be garbage collected
			// automatically because we set OwnerReference.
			log.Info("AgentDeployment not found, likely deleted")
			return ctrl.Result{}, nil
		}
		log.Error(err, "unable to fetch AgentDeployment")
		return ctrl.Result{}, err
	}

	log.Info("Reconciling AgentDeployment",
		"name", agentDeploy.Name,
		"image", agentDeploy.Spec.Container.Image,
	)

	// =========================================================================
	// Step 2: Build the composite version string
	// =========================================================================
	// This captures the "4-layer version identity" that makes agents different
	// from microservices. Used as a label for tracking which version is deployed.
	compositeVersion := buildCompositeVersion(agentDeploy)

	// =========================================================================
	// Step 3: Reconcile the Deployment
	// =========================================================================
	if err := r.reconcileDeployment(ctx, agentDeploy, compositeVersion); err != nil {
		log.Error(err, "failed to reconcile Deployment")
		return ctrl.Result{}, err
	}

	// =========================================================================
	// Step 4: Reconcile the Service
	// =========================================================================
	if err := r.reconcileService(ctx, agentDeploy); err != nil {
		log.Error(err, "failed to reconcile Service")
		return ctrl.Result{}, err
	}

	// =========================================================================
	// Step 5: Update Status
	// =========================================================================
	if err := r.updateStatus(ctx, agentDeploy, compositeVersion); err != nil {
		log.Error(err, "failed to update status")
		return ctrl.Result{}, err
	}

	log.Info("Successfully reconciled AgentDeployment", "name", agentDeploy.Name)
	return ctrl.Result{}, nil
}

// reconcileDeployment creates or updates the Kubernetes Deployment for the agent.
func (r *AgentDeploymentReconciler) reconcileDeployment(
	ctx context.Context,
	agentDeploy *agentrollv1alpha1.AgentDeployment,
	compositeVersion string,
) error {
	log := logf.FromContext(ctx)

	// Determine replica count
	replicas := int32(1)
	if agentDeploy.Spec.Replicas != nil {
		replicas = *agentDeploy.Spec.Replicas
	}

	// Standard labels applied to all resources we create
	labels := map[string]string{
		"app.kubernetes.io/name":          agentDeploy.Name,
		"app.kubernetes.io/managed-by":    "agentroll",
		"agentroll.dev/composite-version": compositeVersion,
	}

	// Add agent meta labels if present
	if agentDeploy.Spec.AgentMeta.PromptVersion != "" {
		labels["agentroll.dev/prompt-version"] = agentDeploy.Spec.AgentMeta.PromptVersion
	}
	if agentDeploy.Spec.AgentMeta.ModelVersion != "" {
		labels["agentroll.dev/model-version"] = agentDeploy.Spec.AgentMeta.ModelVersion
	}

	// Build the desired Deployment
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      agentDeploy.Name,
			Namespace: agentDeploy.Namespace,
		},
	}

	// CreateOrUpdate: if the Deployment exists, update it; if not, create it.
	// This is idempotent — running Reconcile multiple times produces the same result.
	result, err := controllerutil.CreateOrUpdate(ctx, r.Client, deploy, func() error {
		// Set labels
		deploy.Labels = labels

		// Set the pod template
		deploy.Spec = appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app.kubernetes.io/name": agentDeploy.Name,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:      "agent",
							Image:     agentDeploy.Spec.Container.Image,
							Env:       agentDeploy.Spec.Container.Env,
							Ports:     agentDeploy.Spec.Container.Ports,
							Command:   agentDeploy.Spec.Container.Command,
							Args:      agentDeploy.Spec.Container.Args,
							Resources: resourcesOrDefault(agentDeploy.Spec.Container.Resources),
						},
					},
				},
			},
		}

		// Set OwnerReference so the Deployment is garbage collected
		// when the AgentDeployment is deleted.
		return controllerutil.SetControllerReference(agentDeploy, deploy, r.Scheme)
	})

	if err != nil {
		return fmt.Errorf("failed to reconcile Deployment: %w", err)
	}

	log.Info("Deployment reconciled",
		"name", deploy.Name,
		"result", result, // "created", "updated", or "unchanged"
	)
	return nil
}

// reconcileService creates or updates the Kubernetes Service for the agent.
func (r *AgentDeploymentReconciler) reconcileService(
	ctx context.Context,
	agentDeploy *agentrollv1alpha1.AgentDeployment,
) error {
	log := logf.FromContext(ctx)

	// Only create a Service if the agent exposes ports
	if len(agentDeploy.Spec.Container.Ports) == 0 {
		log.Info("No ports defined, skipping Service creation")
		return nil
	}

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      agentDeploy.Name,
			Namespace: agentDeploy.Namespace,
		},
	}

	result, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		svc.Labels = map[string]string{
			"app.kubernetes.io/name":       agentDeploy.Name,
			"app.kubernetes.io/managed-by": "agentroll",
		}
		svc.Spec = corev1.ServiceSpec{
			Selector: map[string]string{
				"app.kubernetes.io/name": agentDeploy.Name,
			},
			Ports: toServicePorts(agentDeploy.Spec.Container.Ports),
		}
		return controllerutil.SetControllerReference(agentDeploy, svc, r.Scheme)
	})

	if err != nil {
		return fmt.Errorf("failed to reconcile Service: %w", err)
	}

	log.Info("Service reconciled",
		"name", svc.Name,
		"result", result,
	)
	return nil
}

// updateStatus writes the current state back to the AgentDeployment status.
func (r *AgentDeploymentReconciler) updateStatus(
	ctx context.Context,
	agentDeploy *agentrollv1alpha1.AgentDeployment,
	compositeVersion string,
) error {
	// For MVP, we set the phase to Stable and record the composite version.
	// In Phase 2, this will integrate with Argo Rollouts to reflect
	// canary progress (Progressing, Degraded, RollingBack).
	agentDeploy.Status.Phase = agentrollv1alpha1.PhaseStable
	agentDeploy.Status.StableVersion = compositeVersion
	agentDeploy.Status.ObservedGeneration = agentDeploy.Generation

	return r.Status().Update(ctx, agentDeploy)
}

// buildCompositeVersion creates a version string from the agent's 4-layer identity.
// This is a key innovation of AgentRoll: tracking the combined state of
// prompt + model + tools as a single deployable version.
func buildCompositeVersion(agentDeploy *agentrollv1alpha1.AgentDeployment) string {
	meta := agentDeploy.Spec.AgentMeta

	prompt := meta.PromptVersion
	if prompt == "" {
		prompt = "default"
	}

	model := meta.ModelVersion
	if model == "" {
		model = "default"
	}

	// Format: prompt:model:imageTag
	// e.g., "abc123:claude-sonnet-4-20250514:v2.1.0"
	imageTag := extractImageTag(agentDeploy.Spec.Container.Image)
	return fmt.Sprintf("%s.%s.%s", prompt, model, imageTag)
}

// extractImageTag gets the tag portion from a container image reference.
// "myregistry/agent:v2.1.0" → "v2.1.0"
// "myregistry/agent" → "latest"
func extractImageTag(image string) string {
	for i := len(image) - 1; i >= 0; i-- {
		if image[i] == ':' {
			return image[i+1:]
		}
		if image[i] == '/' {
			break
		}
	}
	return "latest"
}

// resourcesOrDefault returns the specified resources or sensible defaults.
func resourcesOrDefault(res *corev1.ResourceRequirements) corev1.ResourceRequirements {
	if res != nil {
		return *res
	}
	return corev1.ResourceRequirements{}
}

// toServicePorts converts ContainerPorts to ServicePorts.
func toServicePorts(containerPorts []corev1.ContainerPort) []corev1.ServicePort {
	var svcPorts []corev1.ServicePort
	for _, cp := range containerPorts {
		svcPorts = append(svcPorts, corev1.ServicePort{
			Name:       cp.Name,
			Port:       cp.ContainerPort,
			TargetPort: intstr.FromInt32(cp.ContainerPort),
			Protocol:   cp.Protocol,
		})
	}
	return svcPorts
}

// SetupWithManager sets up the controller with the Manager.
// It watches AgentDeployment resources AND the Deployments/Services it owns.
// If someone manually edits the Deployment, the controller will reconcile it back.
func (r *AgentDeploymentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&agentrollv1alpha1.AgentDeployment{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Named("agentdeployment").
		Complete(r)
}
