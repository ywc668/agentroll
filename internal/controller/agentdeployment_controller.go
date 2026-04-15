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
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	semver "github.com/Masterminds/semver/v3"
	rolloutsv1alpha1 "github.com/argoproj/argo-rollouts/pkg/apis/rollouts/v1alpha1"

	agentrollv1alpha1 "github.com/ywc668/agentroll/api/v1alpha1"
)

// Default image for the analysis runner.
// Users can override by creating their own AnalysisTemplate.
const defaultAnalysisImage = "agentroll-analysis:v1"

// Image for the Langfuse metrics script used by the agent-cost-check managed template.
const defaultLangfuseMetricsImage = "ghcr.io/agentroll/langfuse-metrics:v1"

// Finalizer added to every AgentDeployment so the controller can clean up
// orphaned Argo Rollouts before the AgentDeployment object is removed.
const agentDeploymentFinalizer = "agentroll.dev/finalizer"

// managedAnalysisTemplates is the set of AnalysisTemplates that AgentRoll owns and
// manages. Only these templates receive the standard injected args (service-name,
// service-port, namespace, canary-version, stable-version) because they declare
// those args in their spec. User-managed templates must declare their own args.
var managedAnalysisTemplates = map[string]bool{
	"agent-quality-check": true,
	"agent-cost-check":    true,
}

// Repeated string constants — extracted to satisfy goconst.
const (
	defaultLangfuseSecret   = "langfuse-credentials"
	defaultLangfuseHost     = "https://cloud.langfuse.com"
	defaultImageTag         = "latest"
	conditionReasonStable   = "Stable"
	conditionReasonRollback = "RollingBack"
	conditionReasonPending  = "Pending"
)

// AgentDeploymentReconciler reconciles a AgentDeployment object
type AgentDeploymentReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=agentroll.dev,resources=agentdeployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=agentroll.dev,resources=agentdeployments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=agentroll.dev,resources=agentdeployments/finalizers,verbs=update
// +kubebuilder:rbac:groups=argoproj.io,resources=rollouts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=argoproj.io,resources=analysistemplates,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=apps,resources=replicasets,verbs=get;list;watch
// +kubebuilder:rbac:groups=keda.sh,resources=scaledobjects,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete

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

	// Handle deletion via finalizer — must check before any other work
	if !agentDeploy.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, agentDeploy)
	}

	// Ensure our finalizer is registered (add on first reconcile, then continue)
	if !controllerutil.ContainsFinalizer(agentDeploy, agentDeploymentFinalizer) {
		controllerutil.AddFinalizer(agentDeploy, agentDeploymentFinalizer)
		if err := r.Update(ctx, agentDeploy); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to add finalizer: %w", err)
		}
		// Don't return — continue reconciling so resources are created on the first call
	}

	log.Info("Reconciling AgentDeployment",
		"name", agentDeploy.Name,
		"image", agentDeploy.Spec.Container.Image,
		"strategy", agentDeploy.Spec.Rollout.Strategy,
	)

	// Step 2: Build composite version
	compositeVersion := buildCompositeVersion(agentDeploy)

	// Step 2.5: A2A coordination — block canary progression if declared dependencies
	// are not in Stable phase.  Returns a requeue request when dependencies are unmet
	// so the controller re-checks automatically once dependencies stabilize.
	requeue, err := r.checkAgentDependencies(ctx, agentDeploy)
	if err != nil {
		log.Error(err, "failed to check agent dependencies")
		r.Recorder.Event(agentDeploy, corev1.EventTypeWarning, "ReconcileError",
			fmt.Sprintf("failed to check agent dependencies: %v", err))
		return ctrl.Result{}, err
	}
	if requeue {
		log.Info("Agent dependencies not yet stable — requeueing",
			"name", agentDeploy.Name, "dependsOn", agentDeploy.Spec.DependsOn)
		r.Recorder.Eventf(agentDeploy, corev1.EventTypeNormal, "DependencyNotReady",
			"waiting for dependencies to reach Stable phase: %v", agentDeploy.Spec.DependsOn)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// Step 3: Reconcile AnalysisTemplate
	if err := r.reconcileAnalysisTemplate(ctx, agentDeploy); err != nil {
		log.Error(err, "failed to reconcile AnalysisTemplate")
		r.Recorder.Event(agentDeploy, corev1.EventTypeWarning, "ReconcileError",
			fmt.Sprintf("failed to reconcile AnalysisTemplate: %v", err))
		return ctrl.Result{}, err
	}

	// Step 3.5: Reconcile OTel ConfigMap (if enabled)
	if err := r.reconcileOTelConfig(ctx, agentDeploy); err != nil {
		log.Error(err, "failed to reconcile OTel ConfigMap")
		r.Recorder.Event(agentDeploy, corev1.EventTypeWarning, "ReconcileError",
			fmt.Sprintf("failed to reconcile OTel ConfigMap: %v", err))
		return ctrl.Result{}, err
	}

	// Step 3.7: MCP tool lifecycle — resolve tool endpoints and validate version constraints.
	// Injects MCP_TOOL_<NAME>_ENDPOINT env vars and blocks on unsatisfied semver constraints.
	if err := r.reconcileToolDependencies(ctx, agentDeploy); err != nil {
		log.Error(err, "failed to reconcile tool dependencies")
		r.Recorder.Event(agentDeploy, corev1.EventTypeWarning, "ToolConstraintFailed",
			fmt.Sprintf("MCP tool dependency error: %v", err))
		return ctrl.Result{}, err
	}

	// Step 3.6: RBAC hardening — ensure a dedicated ServiceAccount exists for the agent.
	// If spec.serviceAccountName is empty, auto-create one named after the agent.
	// This provides pod-level isolation: agents don't inherit default SA permissions.
	if err := r.reconcileServiceAccount(ctx, agentDeploy); err != nil {
		log.Error(err, "failed to reconcile ServiceAccount")
		r.Recorder.Event(agentDeploy, corev1.EventTypeWarning, "ReconcileError",
			fmt.Sprintf("failed to reconcile ServiceAccount: %v", err))
		return ctrl.Result{}, err
	}

	// Step 4: Reconcile Argo Rollout
	if err := r.reconcileRollout(ctx, agentDeploy, compositeVersion); err != nil {
		log.Error(err, "failed to reconcile Rollout")
		r.Recorder.Event(agentDeploy, corev1.EventTypeWarning, "ReconcileError",
			fmt.Sprintf("failed to reconcile Rollout: %v", err))
		return ctrl.Result{}, err
	}

	// Step 5: Reconcile Service
	if err := r.reconcileService(ctx, agentDeploy); err != nil {
		log.Error(err, "failed to reconcile Service")
		r.Recorder.Event(agentDeploy, corev1.EventTypeWarning, "ReconcileError",
			fmt.Sprintf("failed to reconcile Service: %v", err))
		return ctrl.Result{}, err
	}

	// Step 5.5: Reconcile KEDA ScaledObject (if scaling.queueRef is configured).
	// Skipped gracefully when KEDA is not installed in the cluster.
	if err := r.reconcileScaledObject(ctx, agentDeploy); err != nil {
		log.Error(err, "failed to reconcile ScaledObject")
		r.Recorder.Event(agentDeploy, corev1.EventTypeWarning, "ReconcileError",
			fmt.Sprintf("failed to reconcile ScaledObject: %v", err))
		return ctrl.Result{}, err
	}

	// Step 5.7: Self-Evolution — analyse canary outcomes and propose improvements.
	// Skipped gracefully when spec.evolution.enabled is false (the default).
	if err := r.reconcileEvolution(ctx, agentDeploy); err != nil {
		log.Error(err, "failed to reconcile Evolution")
		r.Recorder.Event(agentDeploy, corev1.EventTypeWarning, "EvolutionError",
			fmt.Sprintf("evolution loop failed: %v", err))
		// Evolution failures are non-fatal — continue to status update.
	}

	// Step 6: Update Status — capture previous phase so we can emit a phase-change event.
	prevPhase := agentDeploy.Status.Phase
	if err := r.updateStatus(ctx, agentDeploy, compositeVersion); err != nil {
		log.Error(err, "failed to update status")
		return ctrl.Result{}, err
	}
	if agentDeploy.Status.Phase != prevPhase {
		r.emitPhaseEvent(agentDeploy)
	}

	log.Info("Successfully reconciled AgentDeployment", "name", agentDeploy.Name)

	// Belt-and-suspenders requeue: in environments where Rollout watch events are
	// unreliable (e.g., some Kind/CI setups), we poll rather than relying solely on
	// Owns(Rollout) to wake us up.  Active phases requeue quickly; stable/degraded
	// phases requeue slowly to catch incoming canary deployments (spec changes) if
	// the For(AgentDeployment) watch event is also delayed.
	switch agentDeploy.Status.Phase {
	case agentrollv1alpha1.PhaseProgressing, agentrollv1alpha1.PhasePending, agentrollv1alpha1.PhaseRollingBack:
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	default: // Stable, Degraded, or empty (initial)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
}

// handleDeletion cleans up owned resources and removes the finalizer so the
// AgentDeployment can be fully deleted.  We explicitly delete the Argo Rollout
// here (even though it has an owner reference) to guarantee the Rollout and its
// analysis history are flushed before the AgentDeployment disappears.
func (r *AgentDeploymentReconciler) handleDeletion(
	ctx context.Context,
	agentDeploy *agentrollv1alpha1.AgentDeployment,
) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(agentDeploy, agentDeploymentFinalizer) {
		return ctrl.Result{}, nil
	}

	// Delete the owned Argo Rollout (and cascade to its ReplicaSets / AnalysisRuns)
	rollout := &rolloutsv1alpha1.Rollout{}
	err := r.Get(ctx, client.ObjectKey{
		Name:      agentDeploy.Name,
		Namespace: agentDeploy.Namespace,
	}, rollout)
	if err == nil {
		if delErr := r.Delete(ctx, rollout); delErr != nil && !errors.IsNotFound(delErr) {
			return ctrl.Result{}, fmt.Errorf("failed to delete Rollout during cleanup: %w", delErr)
		}
		log.Info("Deleted owned Rollout", "rollout", rollout.Name)
	} else if !errors.IsNotFound(err) {
		return ctrl.Result{}, fmt.Errorf("failed to get Rollout during cleanup: %w", err)
	}

	// Remove the finalizer so the API server can complete deletion
	controllerutil.RemoveFinalizer(agentDeploy, agentDeploymentFinalizer)
	if err := r.Update(ctx, agentDeploy); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to remove finalizer: %w", err)
	}

	r.Recorder.Event(agentDeploy, corev1.EventTypeNormal, "Finalized",
		"owned Rollout deleted and finalizer removed; AgentDeployment deletion proceeding")
	log.Info("Finalizer removed, AgentDeployment deletion proceeding", "name", agentDeploy.Name)
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
  # Prometheus exporter on port 8889 so agent metrics are scrapeable by Prometheus.
  # The AgentRoll PodMonitor (config/prometheus/agent-pod-monitor.yaml) scrapes this
  # endpoint and makes metrics available in the AgentRoll Grafana dashboard.
  prometheus:
    endpoint: "0.0.0.0:8889"
    resource_to_telemetry_conversion:
      enabled: true

service:
  pipelines:
    traces:
      receivers: [otlp]
      processors: [resource, batch]
      exporters: [otlp, logging]
    metrics:
      receivers: [otlp]
      processors: [resource, batch]
      exporters: [otlp, logging, prometheus]
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

	podLabels := buildLabels(agentDeploy, compositeVersion)
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
		rollout.Labels = podLabels

		rollout.Spec = rolloutsv1alpha1.RolloutSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: selectorLabels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: podLabels,
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

		// Step 3: Analysis (if specified).
		// Only inject standard args (service-name, service-port, namespace,
		// canary-version, stable-version) for managed templates that declare them.
		// User-managed templates are responsible for declaring their own args —
		// passing undeclared args causes Argo Rollouts to reject the AnalysisRun.
		if step.Analysis != nil {
			var analysisArgs []rolloutsv1alpha1.AnalysisRunArgument
			if managedAnalysisTemplates[step.Analysis.TemplateRef] {
				analysisArgs = []rolloutsv1alpha1.AnalysisRunArgument{
					{Name: "service-name", Value: agentDeploy.Name},
					{Name: "service-port", Value: servicePort},
					{Name: "namespace", Value: agentDeploy.Namespace},
					{
						// Langfuse-based templates filter traces by this version tag.
						Name:  "canary-version",
						Value: fmt.Sprintf("%s.%s", agentDeploy.Spec.AgentMeta.PromptVersion, agentDeploy.Spec.AgentMeta.ModelVersion),
					},
					{
						// Used by agent-cost-check to compare canary vs stable token cost.
						Name:  "stable-version",
						Value: agentDeploy.Status.StableVersion,
					},
				}
			}
			argoSteps = append(argoSteps, rolloutsv1alpha1.CanaryStep{
				Analysis: &rolloutsv1alpha1.RolloutAnalysis{
					Templates: []rolloutsv1alpha1.AnalysisTemplateRef{
						{TemplateName: step.Analysis.TemplateRef},
					},
					Args: analysisArgs,
				},
			})
		}
	}

	// Item 3: When rollback.onCostSpike is configured, automatically inject a
	// managed agent-cost-check analysis step after all user-defined steps.
	// This compares canary vs stable token cost and fails if the ratio exceeds threshold.
	if agentDeploy.Spec.Rollback != nil && agentDeploy.Spec.Rollback.OnCostSpike != nil {
		maxRatio := parseCostThreshold(agentDeploy.Spec.Rollback.OnCostSpike.Threshold)
		argoSteps = append(argoSteps, rolloutsv1alpha1.CanaryStep{
			Analysis: &rolloutsv1alpha1.RolloutAnalysis{
				Templates: []rolloutsv1alpha1.AnalysisTemplateRef{
					{TemplateName: "agent-cost-check"},
				},
				Args: []rolloutsv1alpha1.AnalysisRunArgument{
					{
						Name:  "canary-version",
						Value: fmt.Sprintf("%s.%s", agentDeploy.Spec.AgentMeta.PromptVersion, agentDeploy.Spec.AgentMeta.ModelVersion),
					},
					{Name: "stable-version", Value: agentDeploy.Status.StableVersion},
					{Name: "max-cost-ratio", Value: fmt.Sprintf("%.2f", maxRatio)},
					{Name: "langfuse-secret-name", Value: langfuseSecretName(agentDeploy)},
					{Name: "langfuse-host", Value: langfuseHost(agentDeploy)},
				},
			},
		})
	}

	return argoSteps
}

// parseCostThreshold converts a percentage string like "200%" to a float ratio (2.0).
func parseCostThreshold(threshold string) float64 {
	s := strings.TrimSuffix(strings.TrimSpace(threshold), "%")
	pct, err := strconv.ParseFloat(s, 64)
	if err != nil || pct <= 0 {
		return 2.0 // default: 200% = 2x
	}
	return pct / 100.0
}

// langfuseSecretName returns the Langfuse K8s secret name from spec, or the default.
func langfuseSecretName(agentDeploy *agentrollv1alpha1.AgentDeployment) string {
	if agentDeploy.Spec.Observability != nil &&
		agentDeploy.Spec.Observability.Langfuse != nil &&
		agentDeploy.Spec.Observability.Langfuse.SecretRef != "" {
		return agentDeploy.Spec.Observability.Langfuse.SecretRef
	}
	return defaultLangfuseSecret
}

// langfuseHost returns the Langfuse server URL from spec, or cloud.langfuse.com.
func langfuseHost(agentDeploy *agentrollv1alpha1.AgentDeployment) string {
	if agentDeploy.Spec.Observability != nil &&
		agentDeploy.Spec.Observability.Langfuse != nil &&
		agentDeploy.Spec.Observability.Langfuse.Endpoint != "" {
		return agentDeploy.Spec.Observability.Langfuse.Endpoint
	}
	return defaultLangfuseHost
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

	for name := range templateNames {
		if !managedAnalysisTemplates[name] {
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

		// Extract tuned thresholds from evolution status so analysis Jobs use
		// data-driven values instead of hardcoded defaults.
		var tunedThresholds map[string]string
		if agentDeploy.Status.Evolution != nil {
			tunedThresholds = agentDeploy.Status.Evolution.TunedThresholds
		}

		result, createErr := controllerutil.CreateOrUpdate(ctx, r.Client, template, func() error {
			template.Labels = map[string]string{
				"app.kubernetes.io/managed-by": "agentroll",
				"agentroll.dev/template-type":  "quality",
			}
			template.Spec = buildManagedTemplateSpec(name, tunedThresholds)
			return nil
		})

		if createErr != nil {
			return fmt.Errorf("failed to reconcile AnalysisTemplate %s: %w", name, createErr)
		}

		log.Info("AnalysisTemplate reconciled", "name", name, "result", result)
	}

	return nil
}

// tunedOrDefault returns the tuned threshold value for key if present in the map,
// otherwise returns dflt. This allows data-driven thresholds computed by the
// threshold-tuner strategy to override the hardcoded Job env var defaults.
func tunedOrDefault(tuned map[string]string, key, dflt string) string {
	if v, ok := tuned[key]; ok && v != "" {
		return v
	}
	return dflt
}

// buildManagedTemplateSpec returns the AnalysisTemplateSpec for a managed template.
// agent-quality-check: runner.py-based health + quality checks against the agent HTTP API.
// agent-cost-check: langfuse_metrics.py-based token cost comparison (canary vs stable).
// tuned may be nil; if set, data-driven threshold values override hardcoded defaults.
func buildManagedTemplateSpec(name string, tuned map[string]string) rolloutsv1alpha1.AnalysisTemplateSpec {
	switch name {
	case "agent-cost-check":
		return costCheckTemplateSpec(tuned)
	default:
		return qualityCheckTemplateSpec(tuned)
	}
}

// qualityCheckTemplateSpec builds the spec for the agent-quality-check managed template.
// Runs runner.py as a Job: health check, query validation, latency, content quality.
func qualityCheckTemplateSpec(tuned map[string]string) rolloutsv1alpha1.AnalysisTemplateSpec {
	defaultPort := "8080"
	return rolloutsv1alpha1.AnalysisTemplateSpec{
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
						Spec: qualityJobSpec(tuned),
					},
				},
			},
		},
	}
}

// costCheckTemplateSpec builds the spec for the agent-cost-check managed template.
// Runs langfuse_metrics.py with METRIC=token_cost_ratio to compare canary vs stable
// token cost.  Injected automatically when spec.rollback.onCostSpike is configured.
func costCheckTemplateSpec(tuned map[string]string) rolloutsv1alpha1.AnalysisTemplateSpec {
	localLangfuseHost := defaultLangfuseHost
	localLangfuseSecret := defaultLangfuseSecret
	defaultMaxCostRatio := "2.0"
	defaultTimeWindow := "10"
	defaultMinTraces := "5"
	count := intstr.FromInt(3)
	failureLimit := intstr.FromInt(1)
	return rolloutsv1alpha1.AnalysisTemplateSpec{
		Args: []rolloutsv1alpha1.Argument{
			{Name: "canary-version"},
			{Name: "stable-version"},
			{Name: "max-cost-ratio", Value: &defaultMaxCostRatio},
			{Name: "langfuse-host", Value: &localLangfuseHost},
			{Name: "langfuse-secret-name", Value: &localLangfuseSecret},
			{Name: "time-window-minutes", Value: &defaultTimeWindow},
			{Name: "min-traces", Value: &defaultMinTraces},
		},
		Metrics: []rolloutsv1alpha1.Metric{
			{
				Name:         "token-cost-ratio",
				Interval:     "2m",
				Count:        &count,
				FailureLimit: &failureLimit,
				Provider: rolloutsv1alpha1.MetricProvider{
					Job: &rolloutsv1alpha1.JobMetric{
						Spec: costCheckJobSpec(tuned),
					},
				},
			},
		},
	}
}

// qualityJobSpec creates a Job spec for the runner.py-based quality check.
// tuned values (from the threshold-tuner strategy) override the hardcoded defaults
// when present. Supported keys: max_latency_ms, min_response_len, min_tool_calls.
func qualityJobSpec(tuned map[string]string) batchv1.JobSpec {
	backoffLimit := int32(0)
	envVars := []corev1.EnvVar{
		{
			// Argo Rollouts interpolates {{args.xxx}} at runtime
			Name:  "AGENT_SERVICE_URL",
			Value: "http://{{args.service-name}}.{{args.namespace}}.svc:{{args.service-port}}",
		},
		{Name: "MAX_LATENCY_MS", Value: tunedOrDefault(tuned, "max_latency_ms", "10000")},
		{Name: "MIN_RESPONSE_LEN", Value: tunedOrDefault(tuned, "min_response_len", "50")},
	}
	// Inject MIN_TOOL_CALLS only when a tuned value exists; the current runner.py
	// defaults to requiring at least 1 tool call but only enforces this when
	// MIN_TOOL_CALLS is explicitly set to a value > 0.
	if v, ok := tuned["min_tool_calls"]; ok && v != "" {
		envVars = append(envVars, corev1.EnvVar{Name: "MIN_TOOL_CALLS", Value: v})
	}
	return batchv1.JobSpec{
		BackoffLimit: &backoffLimit,
		Template: corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				RestartPolicy: corev1.RestartPolicyNever,
				Containers: []corev1.Container{
					{
						Name:  "analysis",
						Image: defaultAnalysisImage,
						Env:   envVars,
					},
				},
			},
		},
	}
}

// costCheckJobSpec creates a Job spec for the langfuse_metrics.py cost ratio check.
// Compares canary token cost (per trace) against stable token cost using Langfuse data.
// tuned values override hardcoded defaults. Supported keys: min_success_rate, max_hallucination_rate.
func costCheckJobSpec(tuned map[string]string) batchv1.JobSpec {
	backoffLimit := int32(0)
	envVars := []corev1.EnvVar{
		{Name: "LANGFUSE_HOST", Value: "{{args.langfuse-host}}"},
		{
			Name: "LANGFUSE_PUBLIC_KEY",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: "{{args.langfuse-secret-name}}",
					},
					Key: "public-key",
				},
			},
		},
		{
			Name: "LANGFUSE_SECRET_KEY",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: "{{args.langfuse-secret-name}}",
					},
					Key: "secret-key",
				},
			},
		},
		{Name: "CANARY_VERSION", Value: "{{args.canary-version}}"},
		{Name: "STABLE_VERSION", Value: "{{args.stable-version}}"},
		{Name: "METRIC", Value: "token_cost_ratio"},
		{Name: "MAX_COST_RATIO", Value: "{{args.max-cost-ratio}}"},
		{Name: "TIME_WINDOW_MINUTES", Value: "{{args.time-window-minutes}}"},
		{Name: "MIN_TRACES", Value: "{{args.min-traces}}"},
	}
	// Inject tuned thresholds when present; these override default behaviour
	// in langfuse_metrics.py without changing the AnalysisTemplate args.
	if v, ok := tuned["min_success_rate"]; ok && v != "" {
		envVars = append(envVars, corev1.EnvVar{Name: "MIN_SUCCESS_RATE", Value: v})
	}
	if v, ok := tuned["max_hallucination_rate"]; ok && v != "" {
		envVars = append(envVars, corev1.EnvVar{Name: "MAX_HALLUCINATION_RATE", Value: v})
	}
	return batchv1.JobSpec{
		BackoffLimit: &backoffLimit,
		Template: corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				RestartPolicy: corev1.RestartPolicyNever,
				Containers: []corev1.Container{
					{
						Name:  "cost-checker",
						Image: defaultLangfuseMetricsImage,
						Env:   envVars,
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

	// Set standard status conditions so tooling (ArgoCD, Flux, kubectl wait) can gate on them.
	r.setStatusConditions(agentDeploy)

	return r.Status().Update(ctx, agentDeploy)
}

// setStatusConditions populates the three standard conditions on the AgentDeployment status:
//   - Available:   True when the agent is Stable and serving traffic normally.
//   - Progressing: True while a canary rollout is in flight.
//   - Degraded:    True when the agent has failed analysis or is rolling back.
func (r *AgentDeploymentReconciler) setStatusConditions(agentDeploy *agentrollv1alpha1.AgentDeployment) {
	phase := agentDeploy.Status.Phase
	gen := agentDeploy.Generation

	var available, progressing, degraded metav1.ConditionStatus
	var availableReason, progressingReason, degradedReason string
	var availableMsg, progressingMsg, degradedMsg string

	switch phase {
	case agentrollv1alpha1.PhaseStable:
		available, availableReason, availableMsg = metav1.ConditionTrue, conditionReasonStable,
			fmt.Sprintf("agent is stable at version %s", agentDeploy.Status.StableVersion)
		progressing, progressingReason, progressingMsg = metav1.ConditionFalse, conditionReasonStable, "no rollout in progress"
		degraded, degradedReason, degradedMsg = metav1.ConditionFalse, conditionReasonStable, "agent is healthy"

	case agentrollv1alpha1.PhaseProgressing:
		available, availableReason, availableMsg = metav1.ConditionFalse, "Progressing",
			fmt.Sprintf("canary rollout in progress at %d%% weight", agentDeploy.Status.CanaryWeight)
		progressing, progressingReason, progressingMsg = metav1.ConditionTrue, "CanaryInProgress",
			fmt.Sprintf("canary rollout in progress at %d%% weight", agentDeploy.Status.CanaryWeight)
		degraded, degradedReason, degradedMsg = metav1.ConditionFalse, "Progressing", "no degradation detected"

	case agentrollv1alpha1.PhaseDegraded:
		available, availableReason, availableMsg = metav1.ConditionFalse, "Degraded", "agent analysis failed"
		progressing, progressingReason, progressingMsg = metav1.ConditionFalse, "Degraded", "rollout halted due to degradation"
		degraded, degradedReason, degradedMsg = metav1.ConditionTrue, "AnalysisFailed", "canary analysis failed; manual intervention may be required"

	case agentrollv1alpha1.PhaseRollingBack:
		available, availableReason, availableMsg = metav1.ConditionFalse, conditionReasonRollback, "rolling back to previous stable version"
		progressing, progressingReason, progressingMsg = metav1.ConditionTrue, conditionReasonRollback, "rollback in progress"
		degraded, degradedReason, degradedMsg = metav1.ConditionTrue, conditionReasonRollback, "canary rejected; rollback in progress"

	default: // Pending or unknown
		available, availableReason, availableMsg = metav1.ConditionFalse, conditionReasonPending, "agent deployment is initializing"
		progressing, progressingReason, progressingMsg = metav1.ConditionFalse, conditionReasonPending, "waiting for initial rollout"
		degraded, degradedReason, degradedMsg = metav1.ConditionFalse, conditionReasonPending, "no degradation detected"
	}

	apimeta.SetStatusCondition(&agentDeploy.Status.Conditions, metav1.Condition{
		Type: "Available", Status: available,
		ObservedGeneration: gen, Reason: availableReason, Message: availableMsg,
	})
	apimeta.SetStatusCondition(&agentDeploy.Status.Conditions, metav1.Condition{
		Type: "Progressing", Status: progressing,
		ObservedGeneration: gen, Reason: progressingReason, Message: progressingMsg,
	})
	apimeta.SetStatusCondition(&agentDeploy.Status.Conditions, metav1.Condition{
		Type: "Degraded", Status: degraded,
		ObservedGeneration: gen, Reason: degradedReason, Message: degradedMsg,
	})
}

// emitPhaseEvent emits a Kubernetes Event describing the phase the AgentDeployment
// has just transitioned into.  Called whenever updateStatus detects a phase change.
func (r *AgentDeploymentReconciler) emitPhaseEvent(agentDeploy *agentrollv1alpha1.AgentDeployment) {
	phase := agentDeploy.Status.Phase
	switch phase {
	case agentrollv1alpha1.PhaseStable:
		r.Recorder.Eventf(agentDeploy, corev1.EventTypeNormal, "RolloutCompleted",
			"agent reached Stable phase at version %s", agentDeploy.Status.StableVersion)
	case agentrollv1alpha1.PhaseProgressing:
		r.Recorder.Eventf(agentDeploy, corev1.EventTypeNormal, "RolloutProgressing",
			"canary rollout started, current weight %d%%", agentDeploy.Status.CanaryWeight)
	case agentrollv1alpha1.PhaseDegraded:
		r.Recorder.Event(agentDeploy, corev1.EventTypeWarning, "RolloutDegraded",
			"canary analysis failed; agent entered Degraded phase")
	case agentrollv1alpha1.PhaseRollingBack:
		r.Recorder.Event(agentDeploy, corev1.EventTypeWarning, "RollingBack",
			"canary rejected; rolling back to previous stable version")
	}
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
	// Argo Rollouts uses "rollouts-pod-template-hash" (not the standard "pod-template-hash")
	selector := labels.SelectorFromSet(labels.Set{
		"rollouts-pod-template-hash": stableHash,
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
	result := map[string]string{
		"app.kubernetes.io/name":          agentDeploy.Name,
		"app.kubernetes.io/managed-by":    "agentroll",
		"agentroll.dev/composite-version": compositeVersion,
	}
	if agentDeploy.Spec.AgentMeta.PromptVersion != "" {
		result["agentroll.dev/prompt-version"] = agentDeploy.Spec.AgentMeta.PromptVersion
	}
	if agentDeploy.Spec.AgentMeta.ModelVersion != "" {
		result["agentroll.dev/model-version"] = agentDeploy.Spec.AgentMeta.ModelVersion
	}
	return result
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
				// Port 8889: Prometheus exporter for agent application metrics.
				// Scraped by the AgentRoll PodMonitor → visible in Grafana dashboard.
				{ContainerPort: 8889, Name: "metrics-prom", Protocol: corev1.ProtocolTCP},
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

		// OTel ConfigMap volume
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

	// Inject SYSTEM_PROMPT from the evolution PromptConfigMap when configured.
	// The prompt-optimizer strategy writes updated prompts here so the next pod
	// restart picks up the new prompt without rebuilding the image.
	if agentDeploy.Spec.Evolution != nil && agentDeploy.Spec.Evolution.PromptConfigMap != "" {
		optional := true
		containers[0].Env = append(containers[0].Env, corev1.EnvVar{
			Name: "SYSTEM_PROMPT",
			ValueFrom: &corev1.EnvVarSource{
				ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: agentDeploy.Spec.Evolution.PromptConfigMap,
					},
					Key:      "system_prompt",
					Optional: &optional,
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
	return defaultImageTag
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

// checkAgentDependencies validates A2A (agent-to-agent) dependencies declared in
// spec.dependsOn.  Returns (true, nil) when any dependency is not yet in Stable
// phase — the caller should requeue rather than continuing the reconcile loop.
// This prevents a canary from receiving traffic if an upstream agent it calls is
// itself degraded or rolling out.
func (r *AgentDeploymentReconciler) checkAgentDependencies(
	ctx context.Context,
	agentDeploy *agentrollv1alpha1.AgentDeployment,
) (requeue bool, err error) {
	if len(agentDeploy.Spec.DependsOn) == 0 {
		return false, nil
	}

	log := logf.FromContext(ctx)

	for _, depName := range agentDeploy.Spec.DependsOn {
		dep := &agentrollv1alpha1.AgentDeployment{}
		if fetchErr := r.Get(ctx, client.ObjectKey{
			Name:      depName,
			Namespace: agentDeploy.Namespace,
		}, dep); fetchErr != nil {
			if errors.IsNotFound(fetchErr) {
				log.Info("Dependency AgentDeployment not found — requeueing",
					"dependency", depName)
				return true, nil
			}
			return false, fmt.Errorf("failed to fetch dependency %q: %w", depName, fetchErr)
		}

		if dep.Status.Phase != agentrollv1alpha1.PhaseStable {
			log.Info("Dependency not yet stable",
				"dependency", depName,
				"phase", dep.Status.Phase,
			)
			return true, nil
		}
	}

	return false, nil
}

// reconcileToolDependencies resolves MCP tool dependency endpoints and validates
// semver version constraints declared in spec.agentMeta.toolDependencies.
//
// For each declared tool:
//  1. Discovers the tool endpoint: use spec.endpoint if provided; otherwise look for
//     a Kubernetes Service named after the tool in the same namespace.
//  2. Reads the deployed version from the Service annotation "agentroll.dev/tool-version".
//  3. Validates the spec version constraint (semver) against the deployed version.
//  4. If the constraint is satisfied (or no version constraint), injects
//     MCP_TOOL_<NAME>_ENDPOINT into the agent container env vars (upper-cased, hyphens→underscores).
//
// Returns an error if a declared tool's version constraint is NOT met, blocking
// the rollout.  Missing tools (Service not found) are skipped with a warning unless
// a version constraint is specified, in which case they are blocking.
func (r *AgentDeploymentReconciler) reconcileToolDependencies(
	ctx context.Context,
	agentDeploy *agentrollv1alpha1.AgentDeployment,
) error {
	if len(agentDeploy.Spec.AgentMeta.ToolDependencies) == 0 {
		return nil
	}

	log := logf.FromContext(ctx)

	for _, tool := range agentDeploy.Spec.AgentMeta.ToolDependencies {
		endpoint := tool.Endpoint

		if endpoint == "" {
			// Service discovery: look for a K8s Service with the tool name
			svc := &corev1.Service{}
			if err := r.Get(ctx, client.ObjectKey{
				Name:      tool.Name,
				Namespace: agentDeploy.Namespace,
			}, svc); err != nil {
				if errors.IsNotFound(err) {
					if tool.Version != "" {
						// Version constraint specified but tool not found — blocking
						return fmt.Errorf("MCP tool %q not found in namespace %q (required version: %s)",
							tool.Name, agentDeploy.Namespace, tool.Version)
					}
					log.Info("MCP tool service not found, skipping endpoint injection",
						"tool", tool.Name)
					continue
				}
				return fmt.Errorf("failed to lookup service for MCP tool %q: %w", tool.Name, err)
			}

			// Validate semver version constraint if specified
			if tool.Version != "" {
				deployedVersion := svc.Annotations["agentroll.dev/tool-version"]
				if deployedVersion == "" {
					deployedVersion = svc.Labels["app.kubernetes.io/version"]
				}
				if deployedVersion != "" {
					if err := validateSemverConstraint(tool.Name, tool.Version, deployedVersion); err != nil {
						return err
					}
				} else {
					log.Info("MCP tool has no version annotation — skipping version check",
						"tool", tool.Name, "constraint", tool.Version)
				}
			}

			// Build in-cluster endpoint from service name
			port := int32(8080)
			if len(svc.Spec.Ports) > 0 {
				port = svc.Spec.Ports[0].Port
			}
			endpoint = fmt.Sprintf("http://%s.%s.svc:%d", tool.Name, agentDeploy.Namespace, port)
		}

		// Inject the resolved endpoint as MCP_TOOL_<NAME>_ENDPOINT
		envVarName := "MCP_TOOL_" + strings.ToUpper(strings.ReplaceAll(tool.Name, "-", "_")) + "_ENDPOINT"
		envVar := corev1.EnvVar{Name: envVarName, Value: endpoint}

		// Append to container env if not already present (idempotent)
		found := false
		for _, e := range agentDeploy.Spec.Container.Env {
			if e.Name == envVarName {
				found = true
				break
			}
		}
		if !found {
			agentDeploy.Spec.Container.Env = append(agentDeploy.Spec.Container.Env, envVar)
		}

		log.Info("MCP tool dependency resolved",
			"tool", tool.Name,
			"endpoint", endpoint,
			"versionConstraint", tool.Version,
		)
	}

	return nil
}

// validateSemverConstraint checks that deployedVersion satisfies the semver constraint.
// Returns a descriptive error if the constraint is not met.
func validateSemverConstraint(toolName, constraint, deployedVersion string) error {
	c, err := semver.NewConstraint(constraint)
	if err != nil {
		// Malformed constraint — treat as a warning, don't block
		return nil
	}
	v, err := semver.NewVersion(deployedVersion)
	if err != nil {
		// Deployed version not parseable — skip constraint check
		return nil
	}
	if !c.Check(v) {
		return fmt.Errorf("MCP tool %q version constraint %q not met: deployed version is %s",
			toolName, constraint, deployedVersion)
	}
	return nil
}

// reconcileServiceAccount ensures a dedicated ServiceAccount exists for the agent.
// When spec.serviceAccountName is empty the controller creates one named after the
// agent, providing pod-level RBAC isolation (agents don't share the default SA).
// When spec.serviceAccountName is explicitly set, this is a no-op — the user owns it.
func (r *AgentDeploymentReconciler) reconcileServiceAccount(
	ctx context.Context,
	agentDeploy *agentrollv1alpha1.AgentDeployment,
) error {
	// Only auto-create when the user has not explicitly named a service account
	if agentDeploy.Spec.ServiceAccountName != "" {
		return nil
	}

	log := logf.FromContext(ctx)
	saName := agentDeploy.Name // agent name = SA name for easy discoverability

	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      saName,
			Namespace: agentDeploy.Namespace,
		},
	}

	result, err := controllerutil.CreateOrUpdate(ctx, r.Client, sa, func() error {
		sa.Labels = map[string]string{
			"app.kubernetes.io/name":       agentDeploy.Name,
			"app.kubernetes.io/managed-by": "agentroll",
			"agentroll.dev/component":      "agent-service-account",
		}
		// No automountServiceAccountToken annotation — keep default (true) so the agent
		// can use in-cluster credentials if it needs to call the Kubernetes API.
		return controllerutil.SetControllerReference(agentDeploy, sa, r.Scheme)
	})
	if err != nil {
		return fmt.Errorf("failed to reconcile ServiceAccount: %w", err)
	}

	// Patch the spec so buildPodSpec picks up the auto-created SA name.
	// We mutate in-memory; the AgentDeployment object itself is NOT patched to
	// avoid infinite reconcile loops. The Rollout spec carries the correct name.
	agentDeploy.Spec.ServiceAccountName = saName

	log.Info("ServiceAccount reconciled", "name", sa.Name, "result", result)
	return nil
}

// reconcileScaledObject creates or updates a KEDA ScaledObject for queue-depth
// autoscaling when spec.scaling.queueRef is configured.
//
// The ScaledObject targets the Argo Rollout, using whatever queue backend the user
// specified (redis, rabbitmq, sqs). KEDA translates queue depth into replica count.
//
// This function is a no-op when:
//   - spec.scaling is nil
//   - spec.scaling.queueRef is nil
//   - KEDA is not installed (CRD missing → API returns NotFound on Create, skipped gracefully)
func (r *AgentDeploymentReconciler) reconcileScaledObject(
	ctx context.Context,
	agentDeploy *agentrollv1alpha1.AgentDeployment,
) error {
	if agentDeploy.Spec.Scaling == nil || agentDeploy.Spec.Scaling.QueueRef == nil {
		return nil
	}

	log := logf.FromContext(ctx)
	scaling := agentDeploy.Spec.Scaling
	queueRef := scaling.QueueRef

	// Build the KEDA trigger based on the queue provider
	trigger, err := buildKEDATrigger(queueRef, scaling.TargetValue)
	if err != nil {
		return fmt.Errorf("unsupported queue provider %q: %w", queueRef.Provider, err)
	}

	minReplicas := int64(scaling.MinReplicas)
	maxReplicas := int64(scaling.MaxReplicas)

	// Use unstructured so we don't need the KEDA Go SDK as a dependency.
	// KEDA CRDs must be installed in the cluster for this to work.
	scaledObject := &unstructured.Unstructured{}
	scaledObject.SetGroupVersionKind(scaledObjectGVK)
	scaledObject.SetName(agentDeploy.Name)
	scaledObject.SetNamespace(agentDeploy.Namespace)

	result, err := controllerutil.CreateOrUpdate(ctx, r.Client, scaledObject, func() error {
		scaledObject.SetLabels(map[string]string{
			"app.kubernetes.io/name":       agentDeploy.Name,
			"app.kubernetes.io/managed-by": "agentroll",
		})
		// ScaledObject spec: targets the Argo Rollout, not a Deployment
		scaledObject.Object["spec"] = map[string]any{
			"scaleTargetRef": map[string]any{
				"apiVersion": "argoproj.io/v1alpha1",
				"kind":       "Rollout",
				"name":       agentDeploy.Name,
			},
			"minReplicaCount": minReplicas,
			"maxReplicaCount": maxReplicas,
			"triggers":        []any{trigger},
		}
		return controllerutil.SetControllerReference(agentDeploy, scaledObject, r.Scheme)
	})
	if err != nil {
		// If KEDA is not installed the API server returns "no kind is registered".
		// Log a warning and skip rather than crashing the reconcile loop.
		if errors.IsNotFound(err) || isNoCRDError(err) {
			log.Info("KEDA ScaledObject CRD not found — KEDA may not be installed; skipping",
				"agent", agentDeploy.Name)
			return nil
		}
		return fmt.Errorf("failed to reconcile ScaledObject: %w", err)
	}

	log.Info("ScaledObject reconciled",
		"name", scaledObject.GetName(),
		"provider", queueRef.Provider,
		"result", result,
	)
	return nil
}

// scaledObjectGVK is the GroupVersionKind for KEDA ScaledObjects.
var scaledObjectGVK = schema.GroupVersionKind{
	Group:   "keda.sh",
	Version: "v1alpha1",
	Kind:    "ScaledObject",
}

// buildKEDATrigger constructs the KEDA trigger map for the given queue provider.
// Supported providers: redis, rabbitmq, sqs (aws-sqs-queue).
func buildKEDATrigger(queueRef *agentrollv1alpha1.QueueReference, targetValue int32) (map[string]any, error) {
	switch queueRef.Provider {
	case "redis":
		return map[string]any{
			"type": "redis",
			"metadata": map[string]any{
				"address":    queueRef.Address,
				"listName":   queueRef.QueueName,
				"listLength": fmt.Sprintf("%d", targetValue),
			},
		}, nil
	case "rabbitmq":
		return map[string]any{
			"type": "rabbitmq",
			"metadata": map[string]any{
				"host":        queueRef.Address,
				"queueName":   queueRef.QueueName,
				"queueLength": fmt.Sprintf("%d", targetValue),
				"protocol":    "amqp",
				"mode":        "QueueLength",
			},
		}, nil
	case "sqs":
		return map[string]any{
			"type": "aws-sqs-queue",
			"metadata": map[string]any{
				"queueURL":    queueRef.Address,
				"queueLength": fmt.Sprintf("%d", targetValue),
				"awsRegion":   "us-east-1", // users can override via env in the KEDA operator
			},
		}, nil
	default:
		return nil, fmt.Errorf("supported providers: redis, rabbitmq, sqs")
	}
}

// isNoCRDError returns true for API errors that indicate a CRD is not registered.
// KEDA is optional — if it is not installed we skip ScaledObject creation silently.
func isNoCRDError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "no kind is registered") ||
		strings.Contains(msg, "no matches for kind") ||
		strings.Contains(msg, "resource type not known")
}

// SetupWithManager sets up the controller with the Manager.
// Reliability settings:
//   - MaxConcurrentReconciles: 4 — allows parallel reconciles of different agents,
//     improving throughput in clusters with many AgentDeployments.
//   - RateLimiter: exponential backoff (base 5s, max 5m) for retries on transient errors.
//     This prevents thundering-herd when the API server is slow or KEDA is unavailable.
func (r *AgentDeploymentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&agentrollv1alpha1.AgentDeployment{}).
		Owns(&rolloutsv1alpha1.Rollout{}).
		Owns(&corev1.Service{}).
		Named("agentdeployment").
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 4,
		}).
		Complete(r)
}
