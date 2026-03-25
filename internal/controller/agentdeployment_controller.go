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

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	rolloutsv1alpha1 "github.com/argoproj/argo-rollouts/pkg/apis/rollouts/v1alpha1"

	agentrollv1alpha1 "github.com/ywc668/agentroll/api/v1alpha1"
)

// AgentDeploymentReconciler reconciles a AgentDeployment object
type AgentDeploymentReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// RBAC for our CRD
// +kubebuilder:rbac:groups=agentroll.dev,resources=agentdeployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=agentroll.dev,resources=agentdeployments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=agentroll.dev,resources=agentdeployments/finalizers,verbs=update
//
// RBAC for Argo Rollouts resources (NEW in Sprint 2)
// +kubebuilder:rbac:groups=argoproj.io,resources=rollouts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=argoproj.io,resources=analysistemplates,verbs=get;list;watch;create;update;patch;delete
//
// RBAC for Services (unchanged from Sprint 1)
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete

// Reconcile is the main reconciliation loop.
//
// Sprint 2 change: instead of creating a native Deployment, we now create
// an Argo Rollout with canary strategy and AnalysisTemplate references.
// This is the key transition from "just deploying" to "progressive delivery."
func (r *AgentDeploymentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Step 1: Fetch the AgentDeployment
	agentDeploy := &agentrollv1alpha1.AgentDeployment{}
	if err := r.Get(ctx, req.NamespacedName, agentDeploy); err != nil {
		if errors.IsNotFound(err) {
			log.Info("AgentDeployment not found, likely deleted")
			return ctrl.Result{}, nil
		}
		log.Error(err, "unable to fetch AgentDeployment")
		return ctrl.Result{}, err
	}

	log.Info("Reconciling AgentDeployment",
		"name", agentDeploy.Name,
		"image", agentDeploy.Spec.Container.Image,
		"strategy", agentDeploy.Spec.Rollout.Strategy,
	)

	// Step 2: Build composite version
	compositeVersion := buildCompositeVersion(agentDeploy)

	// Step 3: Reconcile AnalysisTemplate (NEW in Sprint 2)
	// Ensures agent-quality-check AnalysisTemplate exists in the namespace.
	if err := r.reconcileAnalysisTemplate(ctx, agentDeploy); err != nil {
		log.Error(err, "failed to reconcile AnalysisTemplate")
		return ctrl.Result{}, err
	}

	// Step 4: Reconcile the Argo Rollout (CHANGED from Deployment in Sprint 1)
	if err := r.reconcileRollout(ctx, agentDeploy, compositeVersion); err != nil {
		log.Error(err, "failed to reconcile Rollout")
		return ctrl.Result{}, err
	}

	// Step 5: Reconcile Service (unchanged)
	if err := r.reconcileService(ctx, agentDeploy); err != nil {
		log.Error(err, "failed to reconcile Service")
		return ctrl.Result{}, err
	}

	// Step 6: Update Status
	if err := r.updateStatus(ctx, agentDeploy, compositeVersion); err != nil {
		log.Error(err, "failed to update status")
		return ctrl.Result{}, err
	}

	log.Info("Successfully reconciled AgentDeployment", "name", agentDeploy.Name)
	return ctrl.Result{}, nil
}

// reconcileRollout creates or updates an Argo Rollout for the agent.
//
// This is the Sprint 2 replacement for reconcileDeployment.
// Instead of a plain Kubernetes Deployment, we create an Argo Rollout
// with canary strategy — enabling progressive delivery with evaluation gates.
func (r *AgentDeploymentReconciler) reconcileRollout(
	ctx context.Context,
	agentDeploy *agentrollv1alpha1.AgentDeployment,
	compositeVersion string,
) error {
	log := logf.FromContext(ctx)

	replicas := int32(1)
	if agentDeploy.Spec.Replicas != nil {
		replicas = *agentDeploy.Spec.Replicas
	}

	labels := buildLabels(agentDeploy, compositeVersion)
	selectorLabels := map[string]string{
		"app.kubernetes.io/name": agentDeploy.Name,
	}

	// Build the desired Argo Rollout
	rollout := &rolloutsv1alpha1.Rollout{
		ObjectMeta: metav1.ObjectMeta{
			Name:      agentDeploy.Name,
			Namespace: agentDeploy.Namespace,
		},
	}

	result, err := controllerutil.CreateOrUpdate(ctx, r.Client, rollout, func() error {
		rollout.Labels = labels

		rollout.Spec = rolloutsv1alpha1.RolloutSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: selectorLabels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: agentDeploy.Spec.ServiceAccountName,
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
			// This is the key difference from a Deployment:
			// Argo Rollout strategy with canary steps
			Strategy: buildArgoStrategy(agentDeploy),
		}

		return controllerutil.SetControllerReference(agentDeploy, rollout, r.Scheme)
	})

	if err != nil {
		return fmt.Errorf("failed to reconcile Rollout: %w", err)
	}

	log.Info("Rollout reconciled",
		"name", rollout.Name,
		"result", result,
		"strategy", agentDeploy.Spec.Rollout.Strategy,
	)
	return nil
}

// buildArgoStrategy translates our AgentDeployment rollout config
// into Argo Rollouts' strategy format.
//
// Key translation: our RolloutStep combines setWeight + pause + analysis
// into a single struct. Argo Rollouts uses separate steps for each action.
// So one of our steps may become 1-3 Argo steps.
//
// Example:
//
//	Our step:  { setWeight: 20, pause: {duration: "5m"}, analysis: {templateRef: "agent-quality-check"} }
//	Argo steps: [ {setWeight: 20}, {pause: {duration: 5m}}, {analysis: {templates: [{templateName: "agent-quality-check"}]}} ]
func buildArgoStrategy(agentDeploy *agentrollv1alpha1.AgentDeployment) rolloutsv1alpha1.RolloutStrategy {
	if agentDeploy.Spec.Rollout.Strategy == "blueGreen" {
		// Blue-green support is planned for Phase 3.
		// For now, fall through to canary as default.
		return rolloutsv1alpha1.RolloutStrategy{
			BlueGreen: &rolloutsv1alpha1.BlueGreenStrategy{},
		}
	}

	// Translate our steps to Argo canary steps
	argoSteps := translateSteps(agentDeploy.Spec.Rollout.Steps)

	return rolloutsv1alpha1.RolloutStrategy{
		Canary: &rolloutsv1alpha1.CanaryStrategy{
			Steps: argoSteps,
		},
	}
}

// translateSteps converts AgentRoll's combined steps into Argo Rollouts'
// sequential step format.
//
// This function handles the impedance mismatch between our user-friendly
// "one step = weight + pause + analysis" model and Argo's "each action is
// a separate step" model. Our model is better for users (less YAML),
// Argo's model is more flexible (arbitrary step ordering).
func translateSteps(steps []agentrollv1alpha1.RolloutStep) []rolloutsv1alpha1.CanaryStep {
	argoSteps := make([]rolloutsv1alpha1.CanaryStep, 0, len(steps)*3)

	for _, step := range steps {
		// Step 1: Set traffic weight
		weight := step.SetWeight
		argoSteps = append(argoSteps, rolloutsv1alpha1.CanaryStep{
			SetWeight: &weight,
		})

		// Step 2: Pause (if specified)
		if step.Pause != nil && step.Pause.Duration != "" {
			argoSteps = append(argoSteps, rolloutsv1alpha1.CanaryStep{
				Pause: &rolloutsv1alpha1.RolloutPause{
					Duration: parseDuration(step.Pause.Duration),
				},
			})
		}

		// Step 3: Analysis (if specified)
		if step.Analysis != nil {
			argoSteps = append(argoSteps, rolloutsv1alpha1.CanaryStep{
				Analysis: &rolloutsv1alpha1.RolloutAnalysis{
					Templates: []rolloutsv1alpha1.AnalysisTemplateRef{
						{
							TemplateName: step.Analysis.TemplateRef,
						},
					},
				},
			})
		}
	}

	return argoSteps
}

// parseDuration converts a duration string like "5m" to the Argo Rollouts
// intstr format. Argo uses intstr.IntOrString for pause durations.
func parseDuration(d string) *intstr.IntOrString {
	duration := intstr.FromString(d)
	return &duration
}

// reconcileAnalysisTemplate ensures a basic agent-quality-check
// AnalysisTemplate exists in the agent's namespace.
//
// For MVP, we create a simple template that checks a web endpoint
// for agent quality metrics. In Phase 3, this will be configurable
// and support multiple observability backends (Langfuse, Arize, etc.).
func (r *AgentDeploymentReconciler) reconcileAnalysisTemplate(
	ctx context.Context,
	agentDeploy *agentrollv1alpha1.AgentDeployment,
) error {
	log := logf.FromContext(ctx)

	// Check if any steps reference an analysis template
	hasAnalysis := false
	for _, step := range agentDeploy.Spec.Rollout.Steps {
		if step.Analysis != nil {
			hasAnalysis = true
			break
		}
	}
	if !hasAnalysis {
		return nil
	}

	// Collect unique template names referenced by steps
	templateNames := map[string]bool{}
	for _, step := range agentDeploy.Spec.Rollout.Steps {
		if step.Analysis != nil {
			templateNames[step.Analysis.TemplateRef] = true
		}
	}

	// Pre-built templates that AgentRoll manages automatically.
	// Users can override by creating their own template with the same name
	// (without the agentroll managed-by label).
	managedTemplates := map[string]bool{
		"agent-quality-check": true,
		"agent-cost-check":    true,
	}

	for name := range templateNames {
		// If not a managed template name, assume user created it — skip
		if !managedTemplates[name] {
			log.Info("AnalysisTemplate is user-managed, skipping",
				"name", name,
			)
			continue
		}

		// Check if template already exists
		existing := &rolloutsv1alpha1.AnalysisTemplate{}
		err := r.Get(ctx, client.ObjectKey{
			Name:      name,
			Namespace: agentDeploy.Namespace,
		}, existing)

		if err == nil {
			// Template exists — check if it's ours or user-created
			managedBy, hasLabel := existing.Labels["app.kubernetes.io/managed-by"]
			if !hasLabel || managedBy != "agentroll" {
				// User created or modified this template — don't overwrite
				log.Info("AnalysisTemplate exists but not managed by AgentRoll, skipping",
					"name", name,
				)
				continue
			}
		}

		// Either doesn't exist or is managed by us — create/update
		template := &rolloutsv1alpha1.AnalysisTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: agentDeploy.Namespace,
			},
		}

		result, createErr := controllerutil.CreateOrUpdate(ctx, r.Client, template, func() error {
			template.Labels = map[string]string{
				"app.kubernetes.io/managed-by": "agentroll",
				"agentroll.dev/template-type":  "quality",
			}
			template.Spec = rolloutsv1alpha1.AnalysisTemplateSpec{
				Metrics: []rolloutsv1alpha1.Metric{
					{
						Name:             "agent-health",
						SuccessCondition: "result[0] == 1",
						Provider: rolloutsv1alpha1.MetricProvider{
							Job: &rolloutsv1alpha1.JobMetric{
								Spec: batchJobSpec(),
							},
						},
					},
				},
			}
			return nil
		})

		if createErr != nil {
			return fmt.Errorf("failed to reconcile AnalysisTemplate %s: %w", name, createErr)
		}

		log.Info("AnalysisTemplate reconciled",
			"name", name,
			"result", result,
		)
	}

	return nil
}

// batchJobSpec creates a simple Job that always succeeds.
// This is a placeholder for MVP — it proves the Argo Analysis
// integration works. Real implementations will query Langfuse,
// Prometheus, or custom endpoints for agent quality metrics.
func batchJobSpec() batchv1.JobSpec {
	backoffLimit := int32(0)
	return batchv1.JobSpec{
		BackoffLimit: &backoffLimit,
		Template: corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				RestartPolicy: corev1.RestartPolicyNever,
				Containers: []corev1.Container{
					{
						Name:    "analysis",
						Image:   "busybox:latest",
						Command: []string{"sh", "-c", "echo '[1]'"},
					},
				},
			},
		},
	}
}

// reconcileService creates or updates the Kubernetes Service for the agent.
// Unchanged from Sprint 1.
func (r *AgentDeploymentReconciler) reconcileService(
	ctx context.Context,
	agentDeploy *agentrollv1alpha1.AgentDeployment,
) error {
	log := logf.FromContext(ctx)

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

	log.Info("Service reconciled", "name", svc.Name, "result", result)
	return nil
}

// updateStatus writes the current state back to the AgentDeployment status.
func (r *AgentDeploymentReconciler) updateStatus(
	ctx context.Context,
	agentDeploy *agentrollv1alpha1.AgentDeployment,
	compositeVersion string,
) error {
	// TODO: In Phase 3, read the Argo Rollout status to determine
	// the actual phase (Progressing during canary, Degraded on failure, etc.)
	agentDeploy.Status.Phase = agentrollv1alpha1.PhaseStable
	agentDeploy.Status.StableVersion = compositeVersion
	agentDeploy.Status.ObservedGeneration = agentDeploy.Generation

	return r.Status().Update(ctx, agentDeploy)
}

// ============================================================
// Helper functions
// ============================================================

// buildLabels creates the standard label set for all resources.
func buildLabels(agentDeploy *agentrollv1alpha1.AgentDeployment, compositeVersion string) map[string]string {
	labels := map[string]string{
		"app.kubernetes.io/name":          agentDeploy.Name,
		"app.kubernetes.io/managed-by":    "agentroll",
		"agentroll.dev/composite-version": compositeVersion,
	}
	if agentDeploy.Spec.AgentMeta.PromptVersion != "" {
		labels["agentroll.dev/prompt-version"] = agentDeploy.Spec.AgentMeta.PromptVersion
	}
	if agentDeploy.Spec.AgentMeta.ModelVersion != "" {
		labels["agentroll.dev/model-version"] = agentDeploy.Spec.AgentMeta.ModelVersion
	}
	return labels
}

// buildCompositeVersion creates a version string from the agent's 4-layer identity.
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
	imageTag := extractImageTag(agentDeploy.Spec.Container.Image)
	return fmt.Sprintf("%s.%s.%s", prompt, model, imageTag)
}

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

func resourcesOrDefault(res *corev1.ResourceRequirements) corev1.ResourceRequirements {
	if res != nil {
		return *res
	}
	return corev1.ResourceRequirements{}
}

func toServicePorts(containerPorts []corev1.ContainerPort) []corev1.ServicePort {
	svcPorts := make([]corev1.ServicePort, 0, len(containerPorts))
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
// Sprint 2 change: Owns Rollout instead of Deployment.
func (r *AgentDeploymentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&agentrollv1alpha1.AgentDeployment{}).
		Owns(&rolloutsv1alpha1.Rollout{}).
		Owns(&corev1.Service{}).
		Named("agentdeployment").
		Complete(r)
}
