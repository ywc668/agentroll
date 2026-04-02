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
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	rolloutsv1alpha1 "github.com/argoproj/argo-rollouts/pkg/apis/rollouts/v1alpha1"

	agentrollv1alpha1 "github.com/ywc668/agentroll/api/v1alpha1"
)

// Default image for the analysis runner.
// Users can override by creating their own AnalysisTemplate.
const defaultAnalysisImage = "agentroll-analysis:v1"

// AgentDeploymentReconciler reconciles a AgentDeployment object
type AgentDeploymentReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=agentroll.dev,resources=agentdeployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=agentroll.dev,resources=agentdeployments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=agentroll.dev,resources=agentdeployments/finalizers,verbs=update
// +kubebuilder:rbac:groups=argoproj.io,resources=rollouts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=argoproj.io,resources=analysistemplates,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete

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

	// Step 3: Reconcile AnalysisTemplate
	if err := r.reconcileAnalysisTemplate(ctx, agentDeploy); err != nil {
		log.Error(err, "failed to reconcile AnalysisTemplate")
		return ctrl.Result{}, err
	}

	// Step 3.5: Reconcile OTel ConfigMap (if enabled)
	if err := r.reconcileOTelConfig(ctx, agentDeploy); err != nil {
		log.Error(err, "failed to reconcile OTel ConfigMap")
		return ctrl.Result{}, err
	}

	// Step 4: Reconcile Argo Rollout
	if err := r.reconcileRollout(ctx, agentDeploy, compositeVersion); err != nil {
		log.Error(err, "failed to reconcile Rollout")
		return ctrl.Result{}, err
	}

	// Step 5: Reconcile Service
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

// reconcileOTelConfig creates or updates the OTel Collector configuration ConfigMap.
// The sidecar reads this config to know how to receive, process, and export traces.
func (r *AgentDeploymentReconciler) reconcileOTelConfig(
	ctx context.Context,
	agentDeploy *agentrollv1alpha1.AgentDeployment,
) error {
	// Skip if OTel not enabled
	if agentDeploy.Spec.Observability == nil ||
		agentDeploy.Spec.Observability.OpenTelemetry == nil ||
		!agentDeploy.Spec.Observability.OpenTelemetry.Enabled {
		return nil
	}

	log := logf.FromContext(ctx)

	collectorEndpoint := "otel-collector.monitoring:4317"
	if agentDeploy.Spec.Observability.OpenTelemetry.CollectorEndpoint != "" {
		collectorEndpoint = agentDeploy.Spec.Observability.OpenTelemetry.CollectorEndpoint
	}

	// OTel Collector config that:
	// - Receives traces via OTLP (gRPC on 4317, HTTP on 4318)
	// - Batches them for efficiency
	// - Adds agent metadata as resource attributes
	// - Exports to the configured endpoint
	otelConfig := fmt.Sprintf(`receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 0.0.0.0:4317
      http:
        endpoint: 0.0.0.0:4318

processors:
  batch:
    timeout: 5s
    send_batch_size: 256
  resource:
    attributes:
      - key: agentroll.agent.name
        value: "%s"
        action: upsert
      - key: agentroll.prompt.version
        value: "%s"
        action: upsert
      - key: agentroll.model.version
        value: "%s"
        action: upsert
      - key: agentroll.composite.version
        value: "%s"
        action: upsert

exporters:
  otlp:
    endpoint: "%s"
    tls:
      insecure: true
  logging:
    loglevel: info

service:
  pipelines:
    traces:
      receivers: [otlp]
      processors: [resource, batch]
      exporters: [otlp, logging]
    metrics:
      receivers: [otlp]
      processors: [resource, batch]
      exporters: [otlp, logging]
`,
		agentDeploy.Name,
		agentDeploy.Spec.AgentMeta.PromptVersion,
		agentDeploy.Spec.AgentMeta.ModelVersion,
		buildCompositeVersion(agentDeploy),
		collectorEndpoint,
	)

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-otel-config", agentDeploy.Name),
			Namespace: agentDeploy.Namespace,
		},
	}

	result, err := controllerutil.CreateOrUpdate(ctx, r.Client, cm, func() error {
		cm.Labels = map[string]string{
			"app.kubernetes.io/name":       agentDeploy.Name,
			"app.kubernetes.io/managed-by": "agentroll",
			"agentroll.dev/component":      "otel-config",
		}
		cm.Data = map[string]string{
			"config.yaml": otelConfig,
		}
		return controllerutil.SetControllerReference(agentDeploy, cm, r.Scheme)
	})

	if err != nil {
		return fmt.Errorf("failed to reconcile OTel ConfigMap: %w", err)
	}

	log.Info("OTel ConfigMap reconciled", "name", cm.Name, "result", result)
	return nil
}

// reconcileRollout creates or updates an Argo Rollout for the agent.
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
				Spec: buildPodSpec(agentDeploy),
			},
			Strategy: buildArgoStrategy(agentDeploy),
		}

		return controllerutil.SetControllerReference(agentDeploy, rollout, r.Scheme)
	})

	if err != nil {
		return fmt.Errorf("failed to reconcile Rollout: %w", err)
	}

	log.Info("Rollout reconciled", "name", rollout.Name, "result", result)
	return nil
}

// buildArgoStrategy translates AgentDeployment rollout config into Argo strategy.
func buildArgoStrategy(agentDeploy *agentrollv1alpha1.AgentDeployment) rolloutsv1alpha1.RolloutStrategy {
	if agentDeploy.Spec.Rollout.Strategy == "blueGreen" {
		return rolloutsv1alpha1.RolloutStrategy{
			BlueGreen: &rolloutsv1alpha1.BlueGreenStrategy{},
		}
	}

	// Pass agentDeploy to translateSteps so it can inject service info into analysis args
	argoSteps := translateSteps(agentDeploy)

	return rolloutsv1alpha1.RolloutStrategy{
		Canary: &rolloutsv1alpha1.CanaryStrategy{
			Steps: argoSteps,
		},
	}
}

// translateSteps converts AgentRoll's combined steps into Argo's sequential steps.
// Now also injects the agent's service info as Analysis args so the analysis runner
// knows how to reach the agent being tested.
func translateSteps(agentDeploy *agentrollv1alpha1.AgentDeployment) []rolloutsv1alpha1.CanaryStep {
	steps := agentDeploy.Spec.Rollout.Steps
	argoSteps := make([]rolloutsv1alpha1.CanaryStep, 0, len(steps)*3)

	// Determine agent service info for analysis args
	servicePort := "8080" // default
	if len(agentDeploy.Spec.Container.Ports) > 0 {
		servicePort = fmt.Sprintf("%d", agentDeploy.Spec.Container.Ports[0].ContainerPort)
	}

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
		// Now includes args that tell the analysis runner how to reach the agent
		if step.Analysis != nil {
			argoSteps = append(argoSteps, rolloutsv1alpha1.CanaryStep{
				Analysis: &rolloutsv1alpha1.RolloutAnalysis{
					Templates: []rolloutsv1alpha1.AnalysisTemplateRef{
						{
							TemplateName: step.Analysis.TemplateRef,
						},
					},
					Args: []rolloutsv1alpha1.AnalysisRunArgument{
						{
							Name:  "service-name",
							Value: agentDeploy.Name,
						},
						{
							Name:  "service-port",
							Value: servicePort,
						},
						{
							Name:  "namespace",
							Value: agentDeploy.Namespace,
						},
					},
				},
			})
		}
	}

	return argoSteps
}

func parseDuration(d string) *intstr.IntOrString {
	duration := intstr.FromString(d)
	return &duration
}

// reconcileAnalysisTemplate manages the agent quality check AnalysisTemplate.
// 3-layer design: managed defaults, user override, fully custom.
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

	// Collect unique template names
	templateNames := map[string]bool{}
	for _, step := range agentDeploy.Spec.Rollout.Steps {
		if step.Analysis != nil {
			templateNames[step.Analysis.TemplateRef] = true
		}
	}

	// Pre-built templates that AgentRoll manages
	managedTemplates := map[string]bool{
		"agent-quality-check": true,
		"agent-cost-check":    true,
	}

	for name := range templateNames {
		if !managedTemplates[name] {
			log.Info("AnalysisTemplate is user-managed, skipping", "name", name)
			continue
		}

		// Check if template already exists
		existing := &rolloutsv1alpha1.AnalysisTemplate{}
		err := r.Get(ctx, client.ObjectKey{
			Name:      name,
			Namespace: agentDeploy.Namespace,
		}, existing)

		if err == nil {
			managedBy, hasLabel := existing.Labels["app.kubernetes.io/managed-by"]
			if !hasLabel || managedBy != "agentroll" {
				log.Info("AnalysisTemplate exists but not managed by AgentRoll, skipping", "name", name)
				continue
			}
		}

		// Create or update the managed template
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

			// AnalysisTemplate declares args that will be populated by the Rollout
			// when it creates an AnalysisRun. These args are passed in translateSteps().
			defaultPort := "8080"
			template.Spec = rolloutsv1alpha1.AnalysisTemplateSpec{
				Args: []rolloutsv1alpha1.Argument{
					{Name: "service-name"},
					{Name: "service-port", Value: &defaultPort},
					{Name: "namespace"},
				},
				Metrics: []rolloutsv1alpha1.Metric{
					{
						Name: "agent-health",
						// The analysis runner exits 0 on success, non-zero on failure.
						// Argo Rollouts Job metrics use the Job completion status.
						Provider: rolloutsv1alpha1.MetricProvider{
							Job: &rolloutsv1alpha1.JobMetric{
								Spec: analysisJobSpec(),
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

		log.Info("AnalysisTemplate reconciled", "name", name, "result", result)
	}

	return nil
}

// analysisJobSpec creates a Job that runs the AgentRoll analysis runner.
// The runner sends test queries to the agent, validates responses,
// measures latency, and exits with 0 (pass) or 1 (fail).
//
// This replaces the Sprint 2 busybox placeholder with real quality checks.
// The analysis runner image contains a Python script that:
//  1. Checks the agent's /healthz endpoint
//  2. Sends test queries to /query
//  3. Validates response length, latency, and content
//  4. Exits with appropriate status code
func analysisJobSpec() batchv1.JobSpec {
	backoffLimit := int32(0)
	return batchv1.JobSpec{
		BackoffLimit: &backoffLimit,
		Template: corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				RestartPolicy: corev1.RestartPolicyNever,
				Containers: []corev1.Container{
					{
						Name:  "analysis",
						Image: defaultAnalysisImage,
						Env: []corev1.EnvVar{
							{
								// Argo Rollouts interpolates {{args.xxx}} at runtime
								Name:  "AGENT_SERVICE_URL",
								Value: "http://{{args.service-name}}.{{args.namespace}}.svc:{{args.service-port}}",
							},
							{
								Name:  "MAX_LATENCY_MS",
								Value: "10000",
							},
							{
								Name:  "MIN_RESPONSE_LEN",
								Value: "50",
							},
						},
					},
				},
			},
		},
	}
}

// reconcileService creates or updates the Kubernetes Service.
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

// updateStatus reads the Argo Rollout's real status and maps it to AgentDeployment status.
// This is the key improvement from Sprint 2 — instead of always showing "Stable",
// users can now see the actual canary progress via kubectl get agentdeployments.
func (r *AgentDeploymentReconciler) updateStatus(
	ctx context.Context,
	agentDeploy *agentrollv1alpha1.AgentDeployment,
	compositeVersion string,
) error {
	log := logf.FromContext(ctx)

	// Fetch the Argo Rollout to read its real status
	rollout := &rolloutsv1alpha1.Rollout{}
	err := r.Get(ctx, client.ObjectKey{
		Name:      agentDeploy.Name,
		Namespace: agentDeploy.Namespace,
	}, rollout)

	if err != nil {
		if errors.IsNotFound(err) {
			// Rollout doesn't exist yet — we're in initial creation
			agentDeploy.Status.Phase = agentrollv1alpha1.PhasePending
		} else {
			return fmt.Errorf("failed to get Rollout status: %w", err)
		}
	} else {
		// Map Argo Rollout phase to AgentDeployment phase
		agentDeploy.Status.Phase = mapRolloutPhase(rollout)

		// StableVersion tracks the version that is actually serving stable traffic.
		// We must NOT blindly use compositeVersion (from the current spec) because the
		// spec may have been updated to a canary that was subsequently rejected.
		//
		// Rule: if Argo's stable RS matches the current pod hash (fully promoted),
		// the current spec IS the stable version. Otherwise, read it from the
		// stable ReplicaSet's composite-version label set by our controller.
		if rollout.Status.CurrentPodHash != "" &&
			rollout.Status.CurrentPodHash == rollout.Status.StableRS {
			// Canary was fully promoted — current spec is now stable
			agentDeploy.Status.StableVersion = compositeVersion
		} else if rollout.Status.StableRS != "" {
			// Stable RS differs from current (canary in-flight or aborted)
			// Read the composite version label that our controller stamped on the stable RS
			stableVersion := r.stableRSCompositeVersion(ctx, agentDeploy.Namespace, agentDeploy.Name, rollout.Status.StableRS)
			if stableVersion != "" {
				agentDeploy.Status.StableVersion = stableVersion
			}
			// If we couldn't read the RS label, keep whatever was already in status
		} else {
			// No stable RS yet (first deploy) — use current spec
			agentDeploy.Status.StableVersion = compositeVersion
		}

		// Extract canary weight from traffic weights (if available)
		if rollout.Status.Canary.Weights != nil {
			agentDeploy.Status.CanaryWeight = rollout.Status.Canary.Weights.Canary.Weight
		} else {
			agentDeploy.Status.CanaryWeight = 0
		}

		log.Info("Status synced from Rollout",
			"phase", agentDeploy.Status.Phase,
			"stableVersion", agentDeploy.Status.StableVersion,
			"canaryWeight", agentDeploy.Status.CanaryWeight,
		)
	}

	agentDeploy.Status.ObservedGeneration = agentDeploy.Generation

	return r.Status().Update(ctx, agentDeploy)
}

// stableRSCompositeVersion finds the stable ReplicaSet (matched by pod-template-hash)
// owned by the given Rollout and returns the agentroll.dev/composite-version label value.
// Returns "" if the RS cannot be found or the label is absent.
func (r *AgentDeploymentReconciler) stableRSCompositeVersion(
	ctx context.Context,
	namespace string,
	rolloutName string,
	stableHash string,
) string {
	rsList := &appsv1.ReplicaSetList{}
	selector := labels.SelectorFromSet(labels.Set{
		"pod-template-hash": stableHash,
	})
	if err := r.List(ctx, rsList,
		client.InNamespace(namespace),
		client.MatchingLabelsSelector{Selector: selector},
	); err != nil {
		return ""
	}
	for _, rs := range rsList.Items {
		// Confirm the RS is owned by our Rollout to avoid cross-contamination
		for _, ref := range rs.OwnerReferences {
			if ref.Kind == "Rollout" && ref.Name == rolloutName {
				return rs.Spec.Template.Labels["agentroll.dev/composite-version"]
			}
		}
	}
	return ""
}

// mapRolloutPhase translates Argo Rollout's phase string to AgentDeployment phase.
//
// Argo Rollout phases: "Progressing", "Paused", "Healthy", "Degraded"
// AgentDeployment phases: Pending, Progressing, Stable, Degraded, RollingBack
func mapRolloutPhase(rollout *rolloutsv1alpha1.Rollout) agentrollv1alpha1.AgentDeploymentPhase {
	phase := rollout.Status.Phase

	switch phase {
	case rolloutsv1alpha1.RolloutPhaseHealthy:
		return agentrollv1alpha1.PhaseStable
	case rolloutsv1alpha1.RolloutPhaseProgressing:
		return agentrollv1alpha1.PhaseProgressing
	case rolloutsv1alpha1.RolloutPhasePaused:
		// Paused means waiting at a canary step — still progressing from user's perspective
		return agentrollv1alpha1.PhaseProgressing
	case rolloutsv1alpha1.RolloutPhaseDegraded:
		return agentrollv1alpha1.PhaseDegraded
	default:
		// Unknown or empty phase — treat as pending
		return agentrollv1alpha1.PhasePending
	}
}

// ============================================================
// Helper functions
// ============================================================

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

// buildPodSpec constructs the Pod spec, optionally injecting an OTel sidecar.
// When observability.opentelemetry.enabled is true, we:
//  1. Add an OTel Collector sidecar container
//  2. Inject OTEL_EXPORTER_OTLP_ENDPOINT env var into the agent container
//     pointing to the sidecar on localhost:4318
//
// This means the agent just needs to use any OpenTelemetry SDK — the sidecar
// handles collection, batching, and export to the final destination.
func buildPodSpec(agentDeploy *agentrollv1alpha1.AgentDeployment) corev1.PodSpec {
	// Build the agent container
	agentContainer := corev1.Container{
		Name:      "agent",
		Image:     agentDeploy.Spec.Container.Image,
		Env:       agentDeploy.Spec.Container.Env,
		Ports:     agentDeploy.Spec.Container.Ports,
		Command:   agentDeploy.Spec.Container.Command,
		Args:      agentDeploy.Spec.Container.Args,
		Resources: resourcesOrDefault(agentDeploy.Spec.Container.Resources),
	}

	containers := []corev1.Container{agentContainer}
	var volumes []corev1.Volume

	// Inject OTel sidecar if enabled
	if agentDeploy.Spec.Observability != nil &&
		agentDeploy.Spec.Observability.OpenTelemetry != nil &&
		agentDeploy.Spec.Observability.OpenTelemetry.Enabled {

		// Inject OTEL env var into agent container so it sends traces to the sidecar
		otelEndpoint := "http://localhost:4318"
		containers[0].Env = append(containers[0].Env, corev1.EnvVar{
			Name:  "OTEL_EXPORTER_OTLP_ENDPOINT",
			Value: otelEndpoint,
		})

		// Determine export endpoint
		collectorEndpoint := "http://otel-collector.monitoring:4317"
		if agentDeploy.Spec.Observability.OpenTelemetry.CollectorEndpoint != "" {
			collectorEndpoint = agentDeploy.Spec.Observability.OpenTelemetry.CollectorEndpoint
		}

		// OTel Collector sidecar
		sidecar := corev1.Container{
			Name:  "otel-sidecar",
			Image: "otel/opentelemetry-collector-contrib:0.98.0",
			Args:  []string{"--config=/etc/otelcol/config.yaml"},
			Ports: []corev1.ContainerPort{
				{ContainerPort: 4317, Name: "otlp-grpc", Protocol: corev1.ProtocolTCP},
				{ContainerPort: 4318, Name: "otlp-http", Protocol: corev1.ProtocolTCP},
				{ContainerPort: 8888, Name: "metrics", Protocol: corev1.ProtocolTCP},
			},
			Env: []corev1.EnvVar{
				{Name: "OTEL_EXPORT_ENDPOINT", Value: collectorEndpoint},
			},
			VolumeMounts: []corev1.VolumeMount{
				{
					Name:      "otel-config",
					MountPath: "/etc/otelcol",
					ReadOnly:  true,
				},
			},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    mustParseQuantity("10m"),
					corev1.ResourceMemory: mustParseQuantity("64Mi"),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    mustParseQuantity("100m"),
					corev1.ResourceMemory: mustParseQuantity("128Mi"),
				},
			},
		}

		containers = append(containers, sidecar)

		// ConfigMap volume for OTel config
		volumes = append(volumes, corev1.Volume{
			Name: "otel-config",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: fmt.Sprintf("%s-otel-config", agentDeploy.Name),
					},
				},
			},
		})
	}

	return corev1.PodSpec{
		ServiceAccountName: agentDeploy.Spec.ServiceAccountName,
		Containers:         containers,
		Volumes:            volumes,
	}
}

// mustParseQuantity parses a resource quantity string, panics on invalid input.
func mustParseQuantity(s string) resource.Quantity {
	return resource.MustParse(s)
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
func (r *AgentDeploymentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&agentrollv1alpha1.AgentDeployment{}).
		Owns(&rolloutsv1alpha1.Rollout{}).
		Owns(&corev1.Service{}).
		Named("agentdeployment").
		Complete(r)
}
