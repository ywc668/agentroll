//go:build e2e
// +build e2e

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

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ywc668/agentroll/test/utils"
)

// namespace where the project is deployed in
const namespace = "agentroll-system"

// serviceAccountName created for the project
const serviceAccountName = "agentroll-controller-manager"

// metricsServiceName is the name of the metrics service of the project
const metricsServiceName = "agentroll-controller-manager-metrics-service"

// metricsRoleBindingName is the name of the RBAC that will be created to allow get the metrics data
const metricsRoleBindingName = "agentroll-metrics-binding"

var _ = Describe("Manager", Ordered, func() {
	var controllerPodName string

	// Before running the tests, set up the environment by creating the namespace,
	// enforce the restricted security policy to the namespace, installing CRDs,
	// and deploying the controller.
	BeforeAll(func() {
		By("creating manager namespace")
		cmd := exec.Command("kubectl", "create", "ns", namespace)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create namespace")

		By("labeling the namespace to enforce the restricted security policy")
		cmd = exec.Command("kubectl", "label", "--overwrite", "ns", namespace,
			"pod-security.kubernetes.io/enforce=restricted")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to label namespace with restricted policy")

		By("installing CRDs")
		cmd = exec.Command("make", "install")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to install CRDs")

		By("deploying the controller-manager")
		cmd = exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", managerImage))
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to deploy the controller-manager")
	})

	// After all tests have been executed, clean up by undeploying the controller, uninstalling CRDs,
	// and deleting the namespace.
	AfterAll(func() {
		By("cleaning up the curl pod for metrics")
		cmd := exec.Command("kubectl", "delete", "pod", "curl-metrics", "-n", namespace)
		_, _ = utils.Run(cmd)

		By("undeploying the controller-manager")
		cmd = exec.Command("make", "undeploy")
		_, _ = utils.Run(cmd)

		By("uninstalling CRDs")
		cmd = exec.Command("make", "uninstall")
		_, _ = utils.Run(cmd)

		By("removing manager namespace")
		cmd = exec.Command("kubectl", "delete", "ns", namespace)
		_, _ = utils.Run(cmd)
	})

	// After each test, check for failures and collect logs, events,
	// and pod descriptions for debugging.
	AfterEach(func() {
		specReport := CurrentSpecReport()
		if specReport.Failed() {
			By("Fetching controller manager pod logs")
			cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
			controllerLogs, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Controller logs:\n %s", controllerLogs)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Controller logs: %s", err)
			}

			By("Fetching Kubernetes events")
			cmd = exec.Command("kubectl", "get", "events", "-n", namespace, "--sort-by=.lastTimestamp")
			eventsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Kubernetes events:\n%s", eventsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Kubernetes events: %s", err)
			}

			By("Fetching curl-metrics logs")
			cmd = exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
			metricsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Metrics logs:\n %s", metricsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get curl-metrics logs: %s", err)
			}

			By("Fetching controller manager pod description")
			cmd = exec.Command("kubectl", "describe", "pod", controllerPodName, "-n", namespace)
			podDescription, err := utils.Run(cmd)
			if err == nil {
				fmt.Println("Pod description:\n", podDescription)
			} else {
				fmt.Println("Failed to describe controller pod")
			}
		}
	})

	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)

	Context("Manager", func() {
		It("should run successfully", func() {
			By("validating that the controller-manager pod is running as expected")
			verifyControllerUp := func(g Gomega) {
				// Get the name of the controller-manager pod
				cmd := exec.Command("kubectl", "get",
					"pods", "-l", "control-plane=controller-manager",
					"-o", "go-template={{ range .items }}"+
						"{{ if not .metadata.deletionTimestamp }}"+
						"{{ .metadata.name }}"+
						"{{ \"\\n\" }}{{ end }}{{ end }}",
					"-n", namespace,
				)

				podOutput, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve controller-manager pod information")
				podNames := utils.GetNonEmptyLines(podOutput)
				g.Expect(podNames).To(HaveLen(1), "expected 1 controller pod running")
				controllerPodName = podNames[0]
				g.Expect(controllerPodName).To(ContainSubstring("controller-manager"))

				// Validate the pod's status
				cmd = exec.Command("kubectl", "get",
					"pods", controllerPodName, "-o", "jsonpath={.status.phase}",
					"-n", namespace,
				)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Running"), "Incorrect controller-manager pod status")
			}
			Eventually(verifyControllerUp).Should(Succeed())
		})

		It("should ensure the metrics endpoint is serving metrics", func() {
			By("creating a ClusterRoleBinding for the service account to allow access to metrics")
			cmd := exec.Command("kubectl", "create", "clusterrolebinding", metricsRoleBindingName,
				"--clusterrole=agentroll-metrics-reader",
				fmt.Sprintf("--serviceaccount=%s:%s", namespace, serviceAccountName),
			)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create ClusterRoleBinding")

			By("validating that the metrics service is available")
			cmd = exec.Command("kubectl", "get", "service", metricsServiceName, "-n", namespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Metrics service should exist")

			By("getting the service account token")
			token, err := serviceAccountToken()
			Expect(err).NotTo(HaveOccurred())
			Expect(token).NotTo(BeEmpty())

			By("ensuring the controller pod is ready")
			verifyControllerPodReady := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pod", controllerPodName, "-n", namespace,
					"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("True"), "Controller pod not ready")
			}
			Eventually(verifyControllerPodReady, 3*time.Minute, time.Second).Should(Succeed())

			By("verifying that the controller manager is serving the metrics server")
			verifyMetricsServerStarted := func(g Gomega) {
				cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("Serving metrics server"),
					"Metrics server not yet started")
			}
			Eventually(verifyMetricsServerStarted, 3*time.Minute, time.Second).Should(Succeed())

			// +kubebuilder:scaffold:e2e-metrics-webhooks-readiness

			By("creating the curl-metrics pod to access the metrics endpoint")
			cmd = exec.Command("kubectl", "run", "curl-metrics", "--restart=Never",
				"--namespace", namespace,
				"--image=curlimages/curl:latest",
				"--overrides",
				fmt.Sprintf(`{
					"spec": {
						"containers": [{
							"name": "curl",
							"image": "curlimages/curl:latest",
							"command": ["/bin/sh", "-c"],
							"args": [
								"for i in $(seq 1 30); do curl -v -k -H 'Authorization: Bearer %s' https://%s.%s.svc.cluster.local:8443/metrics && exit 0 || sleep 2; done; exit 1"
							],
							"securityContext": {
								"readOnlyRootFilesystem": true,
								"allowPrivilegeEscalation": false,
								"capabilities": {
									"drop": ["ALL"]
								},
								"runAsNonRoot": true,
								"runAsUser": 1000,
								"seccompProfile": {
									"type": "RuntimeDefault"
								}
							}
						}],
						"serviceAccountName": "%s"
					}
				}`, token, metricsServiceName, namespace, serviceAccountName))
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create curl-metrics pod")

			By("waiting for the curl-metrics pod to complete.")
			verifyCurlUp := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pods", "curl-metrics",
					"-o", "jsonpath={.status.phase}",
					"-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Succeeded"), "curl pod in wrong status")
			}
			Eventually(verifyCurlUp, 5*time.Minute).Should(Succeed())

			By("getting the metrics by checking curl-metrics logs")
			verifyMetricsAvailable := func(g Gomega) {
				metricsOutput, err := getMetricsOutput()
				g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve logs from curl pod")
				g.Expect(metricsOutput).NotTo(BeEmpty())
				g.Expect(metricsOutput).To(ContainSubstring("< HTTP/1.1 200 OK"))
			}
			Eventually(verifyMetricsAvailable, 2*time.Minute).Should(Succeed())
		})

		// +kubebuilder:scaffold:e2e-webhooks-checks

		// TODO: Customize the e2e test suite with scenarios specific to your project.
		// Consider applying sample/CR(s) and check their status and/or verifying
		// the reconciliation by using the metrics, i.e.:
		// metricsOutput, err := getMetricsOutput()
		// Expect(err).NotTo(HaveOccurred(), "Failed to retrieve logs from curl pod")
		// Expect(metricsOutput).To(ContainSubstring(
		//    fmt.Sprintf(`controller_runtime_reconcile_total{controller="%s",result="success"} 1`,
		//    strings.ToLower(<Kind>),
		// ))
	})

	// ────────────────────────────────────────────────────────────────────────────
	// Bad canary rejection flow
	//
	// This test validates the full rollback pipeline:
	//   AgentDeployment (canary update) → controller creates Rollout + AnalysisRun
	//   → AnalysisRun fails → Argo Rollouts rolls back → AgentDeployment reflects failure
	//
	// The test uses a deliberately-failing AnalysisTemplate ("always-fail-check")
	// that runs `busybox exit 1` as the Job. This keeps the test deterministic and
	// free of LLM dependencies. The real LLM-based quality gate detection is
	// demonstrated by the bad-canary-demo scenario in examples/k8s-health-agent/.
	//
	// NOTE: Nested inside Describe("Manager") so it always runs after CRDs are
	// installed and the controller is deployed (Manager.BeforeAll handles setup).
	// ────────────────────────────────────────────────────────────────────────────
	Context("Bad canary rejection flow", func() {
	const (
		agentName     = "e2e-canary-agent"
		testNamespace = "default"
		// alwaysFailTemplate is an AnalysisTemplate that unconditionally fails its
		// single measurement, causing an AnalysisRun to be marked Failed immediately.
		alwaysFailTemplate = `
apiVersion: argoproj.io/v1alpha1
kind: AnalysisTemplate
metadata:
  name: always-fail-check
  namespace: default
spec:
  metrics:
    - name: always-fail
      count: 1
      failureLimit: 0
      provider:
        job:
          spec:
            backoffLimit: 0
            template:
              spec:
                restartPolicy: Never
                containers:
                  - name: fail
                    image: busybox:1.36.1
                    command: ["sh", "-c", "exit 1"]
`
		// stableAgentDeployment is the initial (v1) deployment — no analysis, goes Stable directly.
		stableAgentDeployment = `
apiVersion: agentroll.dev/v1alpha1
kind: AgentDeployment
metadata:
  name: e2e-canary-agent
  namespace: default
spec:
  replicas: 1
  container:
    image: busybox:1.36.1
    command: ["sleep", "3600"]
  agentMeta:
    promptVersion: "v1"
    modelVersion: "test-model"
  rollout:
    strategy: canary
    steps:
      - setWeight: 100
`
		// canaryAgentDeployment triggers a canary with the always-fail analysis step.
		canaryAgentDeployment = `
apiVersion: agentroll.dev/v1alpha1
kind: AgentDeployment
metadata:
  name: e2e-canary-agent
  namespace: default
spec:
  replicas: 1
  container:
    image: busybox:1.36.1
    command: ["sleep", "3600"]
  agentMeta:
    promptVersion: "v2"
    modelVersion: "test-model"
  rollout:
    strategy: canary
    steps:
      - setWeight: 30
        analysis:
          templateRef: always-fail-check
      - setWeight: 100
  rollback:
    onFailedAnalysis: true
`
	)

	// applyYAML writes yaml to a temp file and runs kubectl apply.
	applyYAML := func(yaml string) error {
		f, err := os.CreateTemp("", "agentroll-e2e-*.yaml")
		if err != nil {
			return err
		}
		defer os.Remove(f.Name())
		if _, err := f.WriteString(yaml); err != nil {
			return err
		}
		f.Close()
		cmd := exec.Command("kubectl", "apply", "-f", f.Name())
		_, err = utils.Run(cmd)
		return err
	}

	BeforeAll(func() {
		By("applying the always-fail AnalysisTemplate")
		Expect(applyYAML(alwaysFailTemplate)).To(Succeed())

		By("deploying the stable v1 AgentDeployment (no analysis, goes Stable directly)")
		Expect(applyYAML(stableAgentDeployment)).To(Succeed())

		By("triggering initial reconciliation")
		cmd := exec.Command("kubectl", "annotate", "agentdeployment", agentName,
			"-n", testNamespace,
			"--overwrite", "agentroll.dev/reconcile=init")
		_, _ = utils.Run(cmd)

		By("waiting for the Rollout to reach Stable phase")
		waitForRolloutPhase := func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "rollout", agentName,
				"-n", testNamespace,
				"-o", "jsonpath={.status.phase}")
			out, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(out).To(Equal("Healthy"), "Rollout has not reached Healthy/Stable phase yet")
		}
		Eventually(waitForRolloutPhase, 3*time.Minute, 5*time.Second).Should(Succeed())
	})

	AfterAll(func() {
		By("deleting the AgentDeployment")
		cmd := exec.Command("kubectl", "delete", "agentdeployment", agentName,
			"-n", testNamespace, "--ignore-not-found")
		_, _ = utils.Run(cmd)

		By("deleting generated Rollout")
		cmd = exec.Command("kubectl", "delete", "rollout", agentName,
			"-n", testNamespace, "--ignore-not-found")
		_, _ = utils.Run(cmd)

		By("deleting always-fail AnalysisTemplate")
		cmd = exec.Command("kubectl", "delete", "analysistemplate", "always-fail-check",
			"-n", testNamespace, "--ignore-not-found")
		_, _ = utils.Run(cmd)
	})

	SetDefaultEventuallyTimeout(5 * time.Minute)
	SetDefaultEventuallyPollingInterval(5 * time.Second)

	It("should detect the bad canary and roll back automatically", func() {
		By("triggering a canary deployment with the always-fail analysis step")
		Expect(applyYAML(canaryAgentDeployment)).To(Succeed())

		By("forcing a reconcile so the controller picks up the canary spec update")
		cmd := exec.Command("kubectl", "annotate", "agentdeployment", agentName,
			"-n", testNamespace,
			"--overwrite", "agentroll.dev/reconcile=canary")
		_, _ = utils.Run(cmd)

		By("waiting for an AnalysisRun to be created for revision 2")
		var analysisRunName string
		waitForAnalysisRun := func(g Gomega) {
			// Argo Rollouts labels AnalysisRuns with the rollout name
			cmd := exec.Command("kubectl", "get", "analysisruns",
				"-n", testNamespace,
				"-l", fmt.Sprintf("rollouts.argoproj.io/rollout=%s", agentName),
				"-o", `jsonpath={range .items[*]}{.metadata.name}{"\n"}{end}`)
			out, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			lines := utils.GetNonEmptyLines(out)
			g.Expect(lines).NotTo(BeEmpty(), "No AnalysisRun found for rollout %s yet", agentName)
			analysisRunName = lines[0]
		}
		Eventually(waitForAnalysisRun).Should(Succeed())
		_, _ = fmt.Fprintf(GinkgoWriter, "AnalysisRun: %s\n", analysisRunName)

		By("waiting for the AnalysisRun to fail")
		waitForAnalysisRunFailed := func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "analysisrun", analysisRunName,
				"-n", testNamespace,
				"-o", "jsonpath={.status.phase}")
			out, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(out).To(Equal("Failed"), "AnalysisRun %s has not failed yet (phase=%s)", analysisRunName, out)
		}
		Eventually(waitForAnalysisRunFailed, 3*time.Minute, 5*time.Second).Should(Succeed())

		By("asserting the Rollout is Degraded (rolled back) after analysis failure")
		waitForRolloutDegraded := func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "rollout", agentName,
				"-n", testNamespace,
				"-o", "jsonpath={.status.phase}")
			out, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			// Argo Rollouts phase after a failed analysis during canary is "Degraded"
			g.Expect(out).To(Equal("Degraded"),
				"Rollout should be Degraded after AnalysisRun failure (got %s)", out)
		}
		Eventually(waitForRolloutDegraded, 2*time.Minute, 5*time.Second).Should(Succeed())

		By("asserting AgentDeployment status reflects the rollback")
		cmd = exec.Command("kubectl", "get", "agentdeployment", agentName,
			"-n", testNamespace,
			"-o", "jsonpath={.status.phase}")
		phaseOut, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
		// Controller maps Degraded → "RollingBack" or "Degraded" depending on phase
		Expect(phaseOut).NotTo(Equal("Stable"),
			"AgentDeployment should not report Stable after a failed canary")
	})
	}) // end Context("Bad canary rejection flow")
}) // end Describe("Manager")

// serviceAccountToken returns a token for the specified service account in the given namespace.
// It uses the Kubernetes TokenRequest API to generate a token by directly sending a request
// and parsing the resulting token from the API response.
func serviceAccountToken() (string, error) {
	const tokenRequestRawString = `{
		"apiVersion": "authentication.k8s.io/v1",
		"kind": "TokenRequest"
	}`

	// Temporary file to store the token request
	secretName := fmt.Sprintf("%s-token-request", serviceAccountName)
	tokenRequestFile := filepath.Join("/tmp", secretName)
	err := os.WriteFile(tokenRequestFile, []byte(tokenRequestRawString), os.FileMode(0o644))
	if err != nil {
		return "", err
	}

	var out string
	verifyTokenCreation := func(g Gomega) {
		// Execute kubectl command to create the token
		cmd := exec.Command("kubectl", "create", "--raw", fmt.Sprintf(
			"/api/v1/namespaces/%s/serviceaccounts/%s/token",
			namespace,
			serviceAccountName,
		), "-f", tokenRequestFile)

		output, err := cmd.CombinedOutput()
		g.Expect(err).NotTo(HaveOccurred())

		// Parse the JSON output to extract the token
		var token tokenRequest
		err = json.Unmarshal(output, &token)
		g.Expect(err).NotTo(HaveOccurred())

		out = token.Status.Token
	}
	Eventually(verifyTokenCreation).Should(Succeed())

	return out, err
}

// getMetricsOutput retrieves and returns the logs from the curl pod used to access the metrics endpoint.
func getMetricsOutput() (string, error) {
	By("getting the curl-metrics logs")
	cmd := exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
	return utils.Run(cmd)
}

// tokenRequest is a simplified representation of the Kubernetes TokenRequest API response,
// containing only the token field that we need to extract.
type tokenRequest struct {
	Status struct {
		Token string `json:"token"`
	} `json:"status"`
}
