/*
Copyright 2026 AgentRoll Contributors.
Licensed under the MIT License.
*/

package controller

// prompt_experiment.go — Sprint 10: Prompt A/B Testing Loop
//
// Implements:
//   10.1 PromptVariant CRD lifecycle (Pending → Testing → Promoted/Rejected)
//   10.2 Traffic-split via per-experiment ConfigMap + canary pod injection
//   10.3 Welch's t-test auto-promotion when p < 0.05 AND variant mean > control
//   10.4 status.promptLineage — chain of prompt experiment outcomes

import (
	"context"
	"fmt"
	"math"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	agentrollv1alpha1 "github.com/ywc668/agentroll/api/v1alpha1"
)

// variantConfigMapName returns the name of the per-experiment ConfigMap that holds
// the variant system prompt during an active A/B test.
func variantConfigMapName(agentName string) string {
	return agentName + "-prompt-experiment"
}

// ─── 10.1 Experiment lifecycle ────────────────────────────────────────────────

// reconcilePromptExperiment is Step 5.9 in the reconcile loop.
// It advances the prompt A/B experiment state machine for the AgentDeployment.
// No-op when spec.evolution.promptExperiment is empty.
// Non-fatal — experiment failures must never block rollouts.
func (r *AgentDeploymentReconciler) reconcilePromptExperiment(
	ctx context.Context,
	agentDeploy *agentrollv1alpha1.AgentDeployment,
) error {
	ev := agentDeploy.Spec.Evolution
	if ev == nil || ev.PromptExperiment == "" {
		return nil
	}
	log := logf.FromContext(ctx)

	// Fetch the referenced PromptVariant.
	variant := &agentrollv1alpha1.PromptVariant{}
	if err := r.Get(ctx, client.ObjectKey{
		Name:      ev.PromptExperiment,
		Namespace: agentDeploy.Namespace,
	}, variant); err != nil {
		if errors.IsNotFound(err) {
			r.Recorder.Event(agentDeploy, corev1.EventTypeWarning, "PromptExperimentError",
				fmt.Sprintf("PromptVariant %q not found in namespace %q",
					ev.PromptExperiment, agentDeploy.Namespace))
			return nil
		}
		return fmt.Errorf("fetching PromptVariant %q: %w", ev.PromptExperiment, err)
	}

	switch variant.Status.Phase {
	case "", agentrollv1alpha1.PromptVariantPhasePending:
		log.Info("Starting prompt experiment", "variant", variant.Name)
		return r.startExperiment(ctx, agentDeploy, variant)
	case agentrollv1alpha1.PromptVariantPhaseTesting:
		return r.evaluateExperiment(ctx, agentDeploy, variant)
	default:
		// Promoted or Rejected — experiment complete; wait for user to clear promptExperiment.
		return nil
	}
}

// startExperiment transitions a Pending PromptVariant to Testing.
// It creates the per-experiment ConfigMap and records the experiment start time.
func (r *AgentDeploymentReconciler) startExperiment(
	ctx context.Context,
	agentDeploy *agentrollv1alpha1.AgentDeployment,
	variant *agentrollv1alpha1.PromptVariant,
) error {
	log := logf.FromContext(ctx)

	// Create/update the variant ConfigMap with the candidate prompt.
	cmName := variantConfigMapName(agentDeploy.Name)
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cmName,
			Namespace: agentDeploy.Namespace,
		},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, cm, func() error {
		cm.Data = map[string]string{
			"system_prompt": variant.Spec.SystemPrompt,
		}
		return controllerutil.SetControllerReference(agentDeploy, cm, r.Scheme)
	})
	if err != nil {
		return fmt.Errorf("creating variant ConfigMap %q: %w", cmName, err)
	}

	// Transition PromptVariant to Testing.
	now := metav1.Now()
	variant.Status.Phase = agentrollv1alpha1.PromptVariantPhaseTesting
	variant.Status.ExperimentStartedAt = &now
	variant.Status.Message = fmt.Sprintf(
		"Testing started: collecting judge scores (min=%d)",
		promptExperimentMinSamples(agentDeploy))
	if err := r.Status().Update(ctx, variant); err != nil {
		return fmt.Errorf("setting PromptVariant %q to Testing: %w", variant.Name, err)
	}

	log.Info("Prompt experiment started",
		"variant", variant.Name,
		"agent", agentDeploy.Name,
		"variantCM", cmName)
	r.Recorder.Eventf(agentDeploy, corev1.EventTypeNormal, "PromptExperimentStarted",
		"Prompt experiment started: testing variant %q (min samples: %d)",
		variant.Name, promptExperimentMinSamples(agentDeploy))
	return nil
}

// evaluateExperiment collects scores and makes a promotion/rejection decision
// once enough samples have been gathered.
func (r *AgentDeploymentReconciler) evaluateExperiment(
	ctx context.Context,
	agentDeploy *agentrollv1alpha1.AgentDeployment,
	variant *agentrollv1alpha1.PromptVariant,
) error {
	log := logf.FromContext(ctx)

	if variant.Status.ExperimentStartedAt == nil {
		return nil
	}
	startedAt := variant.Status.ExperimentStartedAt.Time
	minSamples := promptExperimentMinSamples(agentDeploy)

	variantScores := collectVariantScores(agentDeploy.Status.EvalHistory, startedAt)
	controlScores := collectControlScores(agentDeploy.Status.EvalHistory, startedAt)

	// Update collected scores in status (non-blocking progress tracking).
	variant.Status.VariantScores = variantScores
	if int32(len(variantScores)) < minSamples {
		log.Info("Awaiting variant scores",
			"variant", variant.Name,
			"collected", len(variantScores), "needed", minSamples)
		if err := r.Status().Update(ctx, variant); err != nil {
			return fmt.Errorf("updating PromptVariant scores: %w", err)
		}
		return nil
	}
	if len(controlScores) < 2 {
		log.Info("Insufficient control scores for t-test — waiting",
			"variant", variant.Name, "control", len(controlScores))
		if err := r.Status().Update(ctx, variant); err != nil {
			return fmt.Errorf("updating PromptVariant scores: %w", err)
		}
		return nil
	}

	// Run Welch's t-test.
	pValue, variantBetter := welchTTest(variantScores, controlScores)
	variantMean, _ := meanAndVariance(variantScores)
	controlMean, _ := meanAndVariance(controlScores)

	variant.Status.VariantMeanScore = variantMean
	variant.Status.ControlMeanScore = controlMean
	variant.Status.PValue = pValue

	log.Info("T-test result",
		"variant", variant.Name,
		"variantMean", fmt.Sprintf("%.3f", variantMean),
		"controlMean", fmt.Sprintf("%.3f", controlMean),
		"p", fmt.Sprintf("%.4f", pValue),
		"variantBetter", variantBetter)

	if variantBetter && pValue < 0.05 {
		return r.promoteVariant(ctx, agentDeploy, variant)
	}
	return r.rejectVariant(ctx, agentDeploy, variant)
}

// promoteVariant writes the winning prompt to the main promptConfigMap,
// sets the variant to Promoted, and appends to status.promptLineage.
func (r *AgentDeploymentReconciler) promoteVariant(
	ctx context.Context,
	agentDeploy *agentrollv1alpha1.AgentDeployment,
	variant *agentrollv1alpha1.PromptVariant,
) error {
	log := logf.FromContext(ctx)

	// Write the winning prompt to the main promptConfigMap (if configured).
	if agentDeploy.Spec.Evolution != nil && agentDeploy.Spec.Evolution.PromptConfigMap != "" {
		mainCM := &corev1.ConfigMap{}
		err := r.Get(ctx, client.ObjectKey{
			Name:      agentDeploy.Spec.Evolution.PromptConfigMap,
			Namespace: agentDeploy.Namespace,
		}, mainCM)
		if err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("fetching main promptConfigMap: %w", err)
		}
		if errors.IsNotFound(err) {
			// Create the main CM if it doesn't exist yet.
			mainCM = &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      agentDeploy.Spec.Evolution.PromptConfigMap,
					Namespace: agentDeploy.Namespace,
				},
			}
		}
		if mainCM.Data == nil {
			mainCM.Data = map[string]string{}
		}
		mainCM.Data["system_prompt"] = variant.Spec.SystemPrompt
		if mainCM.ResourceVersion == "" {
			if err := r.Create(ctx, mainCM); err != nil {
				return fmt.Errorf("creating main promptConfigMap with winning prompt: %w", err)
			}
		} else {
			if err := r.Update(ctx, mainCM); err != nil {
				return fmt.Errorf("updating main promptConfigMap with winning prompt: %w", err)
			}
		}
		log.Info("Winning prompt written to main ConfigMap",
			"configMap", agentDeploy.Spec.Evolution.PromptConfigMap)
	}

	// Transition PromptVariant to Promoted.
	now := metav1.Now()
	variant.Status.Phase = agentrollv1alpha1.PromptVariantPhasePromoted
	variant.Status.PromotedAt = &now
	variant.Status.Message = fmt.Sprintf(
		"Promoted: variant mean %.3f > control mean %.3f with p=%.4f < 0.05",
		variant.Status.VariantMeanScore, variant.Status.ControlMeanScore, variant.Status.PValue)
	if err := r.Status().Update(ctx, variant); err != nil {
		return fmt.Errorf("setting PromptVariant %q to Promoted: %w", variant.Name, err)
	}

	// Append to AgentDeployment's prompt lineage.
	appendPromptLineage(&agentDeploy.Status, agentrollv1alpha1.PromptLineageEntry{
		VariantName:   variant.Name,
		ParentVersion: variant.Spec.ParentVersion,
		Hypothesis:    variant.Spec.Hypothesis,
		VariantMean:   variant.Status.VariantMeanScore,
		ControlMean:   variant.Status.ControlMeanScore,
		PValue:        variant.Status.PValue,
		Outcome:       "promoted",
		At:            now,
	})

	r.Recorder.Event(agentDeploy, corev1.EventTypeNormal, "PromptVariantPromoted",
		variant.Status.Message)
	return nil
}

// rejectVariant sets the variant to Rejected and appends to status.promptLineage.
func (r *AgentDeploymentReconciler) rejectVariant(
	ctx context.Context,
	agentDeploy *agentrollv1alpha1.AgentDeployment,
	variant *agentrollv1alpha1.PromptVariant,
) error {
	now := metav1.Now()
	variant.Status.Phase = agentrollv1alpha1.PromptVariantPhaseRejected
	variant.Status.RejectedAt = &now

	if variant.Status.PValue >= 0.05 {
		variant.Status.Message = fmt.Sprintf(
			"Rejected: p-value %.4f >= 0.05 (not statistically significant)",
			variant.Status.PValue)
	} else {
		variant.Status.Message = fmt.Sprintf(
			"Rejected: variant mean %.3f not better than control mean %.3f",
			variant.Status.VariantMeanScore, variant.Status.ControlMeanScore)
	}

	if err := r.Status().Update(ctx, variant); err != nil {
		return fmt.Errorf("setting PromptVariant %q to Rejected: %w", variant.Name, err)
	}

	appendPromptLineage(&agentDeploy.Status, agentrollv1alpha1.PromptLineageEntry{
		VariantName:   variant.Name,
		ParentVersion: variant.Spec.ParentVersion,
		Hypothesis:    variant.Spec.Hypothesis,
		VariantMean:   variant.Status.VariantMeanScore,
		ControlMean:   variant.Status.ControlMeanScore,
		PValue:        variant.Status.PValue,
		Outcome:       "rejected",
		At:            now,
	})

	r.Recorder.Event(agentDeploy, corev1.EventTypeNormal, "PromptVariantRejected",
		variant.Status.Message)
	return nil
}

// promptExperimentMinSamples returns the configured minimum samples or the default (5).
func promptExperimentMinSamples(agentDeploy *agentrollv1alpha1.AgentDeployment) int32 {
	if agentDeploy.Spec.Evolution != nil &&
		agentDeploy.Spec.Evolution.PromptExperimentMinSamples != nil {
		return *agentDeploy.Spec.Evolution.PromptExperimentMinSamples
	}
	return 5
}

// ─── 10.2 Score collection ────────────────────────────────────────────────────

// collectVariantScores returns quality scores from evalHistory entries whose
// At timestamp is after the experiment start time (i.e., scores from canary pods
// running the variant prompt).
func collectVariantScores(history []agentrollv1alpha1.EvalHistoryEntry, since time.Time) []float64 {
	var scores []float64
	for _, e := range history {
		if e.At.Time.After(since) {
			scores = append(scores, e.QualityScore)
		}
	}
	return scores
}

// collectControlScores returns quality scores from evalHistory entries whose
// At timestamp is before the experiment start time (i.e., baseline scores from
// stable pods running the control prompt).
func collectControlScores(history []agentrollv1alpha1.EvalHistoryEntry, before time.Time) []float64 {
	var scores []float64
	for _, e := range history {
		if e.At.Time.Before(before) {
			scores = append(scores, e.QualityScore)
		}
	}
	return scores
}

// ─── 10.3 Statistical testing ─────────────────────────────────────────────────

// meanAndVariance computes the mean and sample variance (Bessel-corrected) of scores.
// Returns (0, 0) for empty slices and (mean, 0) for single-element slices.
func meanAndVariance(scores []float64) (mean, variance float64) {
	n := len(scores)
	if n == 0 {
		return 0, 0
	}
	for _, s := range scores {
		mean += s
	}
	mean /= float64(n)
	if n < 2 {
		return mean, 0
	}
	for _, s := range scores {
		d := s - mean
		variance += d * d
	}
	variance /= float64(n - 1) // Bessel's correction
	return mean, variance
}

// welchTTest performs Welch's two-sample t-test (two-tailed).
// Returns (pValue, aIsLarger):
//   - pValue: probability of observing this difference by chance; < 0.05 = significant
//   - aIsLarger: true if mean(a) > mean(b)
//
// Returns (1.0, false) when either group has fewer than 2 samples.
func welchTTest(a, b []float64) (float64, bool) {
	if len(a) < 2 || len(b) < 2 {
		return 1.0, false
	}

	meanA, varA := meanAndVariance(a)
	meanB, varB := meanAndVariance(b)

	n1, n2 := float64(len(a)), float64(len(b))
	se1 := varA / n1 // variance of mean estimate for a
	se2 := varB / n2 // variance of mean estimate for b
	se := math.Sqrt(se1 + se2)

	if se == 0 {
		// Both distributions are constant.
		return 1.0, meanA > meanB
	}

	t := (meanA - meanB) / se

	// Welch-Satterthwaite degrees of freedom.
	df := (se1 + se2) * (se1 + se2) /
		(se1*se1/(n1-1) + se2*se2/(n2-1))

	// Two-tailed p-value.
	pValue := tDistPValue(math.Abs(t), df)
	return pValue, meanA > meanB
}

// tDistPValue returns the two-tailed p-value for a Student's t-distribution.
// Uses the relationship: p = I_x(df/2, 1/2) where x = df/(df+t²)
// and I_x is the regularized incomplete beta function.
func tDistPValue(t, df float64) float64 {
	x := df / (df + t*t)
	return regularizedIncompleteBeta(df/2, 0.5, x)
}

// regularizedIncompleteBeta computes I_x(a, b) using the continued fraction
// representation (Lentz algorithm). Switches between two representations
// based on x for numerical stability.
func regularizedIncompleteBeta(a, b, x float64) float64 {
	if x < 0 || x > 1 {
		return 0
	}
	if x == 0 {
		return 0
	}
	if x == 1 {
		return 1
	}
	lbeta := logBeta(a, b)
	if x < (a+1)/(a+b+2) {
		cf := betaContinuedFraction(a, b, x)
		return cf * math.Exp(a*math.Log(x)+b*math.Log(1-x)-math.Log(a)-lbeta)
	}
	cf := betaContinuedFraction(b, a, 1-x)
	return 1 - cf*math.Exp(b*math.Log(1-x)+a*math.Log(x)-math.Log(b)-lbeta)
}

// logBeta computes ln(B(a,b)) = ln(Γ(a)) + ln(Γ(b)) - ln(Γ(a+b)).
func logBeta(a, b float64) float64 {
	lga, _ := math.Lgamma(a)
	lgb, _ := math.Lgamma(b)
	lgab, _ := math.Lgamma(a + b)
	return lga + lgb - lgab
}

// betaContinuedFraction evaluates the continued fraction for the incomplete beta
// function using the modified Lentz method (Numerical Recipes §6.4).
func betaContinuedFraction(a, b, x float64) float64 {
	const (
		maxIter = 200
		eps     = 3e-7
		tiny    = 1e-30
	)

	qab := a + b
	qap := a + 1
	qam := a - 1

	c := 1.0
	d := 1 - qab*x/qap
	if math.Abs(d) < tiny {
		d = tiny
	}
	d = 1 / d
	h := d

	for m := 1; m <= maxIter; m++ {
		mf := float64(m)
		m2 := 2 * mf

		// Even step.
		aa := mf * (b - mf) * x / ((qam + m2) * (a + m2))
		d = 1 + aa*d
		if math.Abs(d) < tiny {
			d = tiny
		}
		c = 1 + aa/c
		if math.Abs(c) < tiny {
			c = tiny
		}
		d = 1 / d
		h *= d * c

		// Odd step.
		aa = -(a + mf) * (qab + mf) * x / ((a + m2) * (qap + m2))
		d = 1 + aa*d
		if math.Abs(d) < tiny {
			d = tiny
		}
		c = 1 + aa/c
		if math.Abs(c) < tiny {
			c = tiny
		}
		d = 1 / d
		delta := d * c
		h *= delta
		if math.Abs(delta-1) < eps {
			break
		}
	}
	return h
}

// ─── 10.4 Prompt lineage ring buffer ─────────────────────────────────────────

// appendPromptLineage appends entry to status.promptLineage, capped at maxPromptLineage.
// Oldest entries are evicted when the buffer is full.
func appendPromptLineage(
	status *agentrollv1alpha1.AgentDeploymentStatus,
	entry agentrollv1alpha1.PromptLineageEntry,
) {
	const maxPromptLineage = 20
	status.PromptLineage = append(status.PromptLineage, entry)
	if len(status.PromptLineage) > maxPromptLineage {
		status.PromptLineage = status.PromptLineage[len(status.PromptLineage)-maxPromptLineage:]
	}
}
