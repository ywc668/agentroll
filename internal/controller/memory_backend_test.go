/*
Copyright 2026 AgentRoll Contributors.
Licensed under the MIT License.
*/

package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	rolloutsv1alpha1 "github.com/argoproj/argo-rollouts/pkg/apis/rollouts/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	agentrollv1alpha1 "github.com/ywc668/agentroll/api/v1alpha1"
)

func TestMemorySnapshotSecretName(t *testing.T) {
	got := memorySnapshotSecretName("my-agent")
	want := "my-agent-memory-snapshot"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMem0ExportMemory_Success(t *testing.T) {
	want := []byte(`{"memories":[{"content":"test data"}]}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/api/memory/export" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("session_id"); got != "sess1" {
			t.Errorf("expected session_id=sess1, got %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer testkey" {
			t.Errorf("expected bearer auth, got %q", got)
		}
		w.Write(want)
	}))
	defer srv.Close()

	got, err := mem0ExportMemory(context.Background(), srv.URL, "sess1", "testkey")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("body: got %s, want %s", got, want)
	}
}

func TestMem0ExportMemory_NoSessionID(t *testing.T) {
	// session_id should be omitted when empty
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("session_id") != "" {
			t.Errorf("expected no session_id, got %q", r.URL.Query().Get("session_id"))
		}
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	_, err := mem0ExportMemory(context.Background(), srv.URL, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMem0ExportMemory_NonOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer srv.Close()

	_, err := mem0ExportMemory(context.Background(), srv.URL, "", "")
	if err == nil {
		t.Fatal("expected error for 500 status, got nil")
	}
}

func TestMem0ImportMemory_Success(t *testing.T) {
	payload := []byte(`{"memories":[{"content":"restored"}]}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/api/memory/import" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected application/json Content-Type, got %q", r.Header.Get("Content-Type"))
		}
		if got := r.URL.Query().Get("session_id"); got != "sess2" {
			t.Errorf("expected session_id=sess2, got %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer importkey" {
			t.Errorf("expected bearer auth, got %q", got)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	err := mem0ImportMemory(context.Background(), srv.URL, "sess2", "importkey", payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMem0ImportMemory_NonOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("invalid payload"))
	}))
	defer srv.Close()

	err := mem0ImportMemory(context.Background(), srv.URL, "", "", []byte("{}"))
	if err == nil {
		t.Fatal("expected error for 400 status, got nil")
	}
}

func TestMem0ImportMemory_204Accepted(t *testing.T) {
	// 204 No Content is also a valid success response
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	err := mem0ImportMemory(context.Background(), srv.URL, "", "", []byte("{}"))
	if err != nil {
		t.Fatalf("unexpected error for 204: %v", err)
	}
}

func makeTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	_ = agentrollv1alpha1.AddToScheme(s)
	return s
}

func TestReadBackendAPIKey_Success(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "mem0-creds", Namespace: "default"},
		Data:       map[string][]byte{"MEM0_API_KEY": []byte("super-secret")},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(makeTestScheme()).WithObjects(secret).Build()
	r := &AgentDeploymentReconciler{Client: fakeClient}

	key, err := r.readBackendAPIKey(context.Background(), "default", "mem0-creds")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "super-secret" {
		t.Errorf("got key %q, want %q", key, "super-secret")
	}
}

func TestReadBackendAPIKey_MissingKey(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "mem0-creds", Namespace: "default"},
		Data:       map[string][]byte{"WRONG_KEY": []byte("value")},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(makeTestScheme()).WithObjects(secret).Build()
	r := &AgentDeploymentReconciler{Client: fakeClient}

	_, err := r.readBackendAPIKey(context.Background(), "default", "mem0-creds")
	if err == nil {
		t.Fatal("expected error for missing MEM0_API_KEY, got nil")
	}
}

func TestRestoreMemorySnapshot_NoSnapshot(t *testing.T) {
	// restoreMemorySnapshot must be a no-op when no snapshot has been recorded
	fakeClient := fake.NewClientBuilder().WithScheme(makeTestScheme()).Build()
	r := &AgentDeploymentReconciler{Client: fakeClient}

	ad := &agentrollv1alpha1.AgentDeployment{
		ObjectMeta: metav1.ObjectMeta{Name: "my-agent", Namespace: "default"},
		Spec: agentrollv1alpha1.AgentDeploymentSpec{
			Memory: &agentrollv1alpha1.MemorySpec{
				Backend: &agentrollv1alpha1.MemoryBackendSpec{
					Endpoint:  "http://mem0.svc",
					SecretRef: "mem0-creds",
				},
			},
		},
		// Status.Memory is nil — no prior snapshot
	}

	err := r.restoreMemorySnapshot(context.Background(), ad)
	if err != nil {
		t.Fatalf("expected no error for missing snapshot, got: %v", err)
	}
}

func TestMem0HTTPClient_HasTimeout(t *testing.T) {
	const want = 30 * time.Second
	if mem0HTTPClient.Timeout != want {
		t.Errorf("mem0HTTPClient.Timeout = %v, want %v", mem0HTTPClient.Timeout, want)
	}
}

// makeRolloutsScheme returns a scheme that includes corev1, agentrollv1alpha1, and
// rolloutsv1alpha1 — required for tests that use the fake client with AnalysisTemplates.
func makeRolloutsScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	_ = agentrollv1alpha1.AddToScheme(s)
	_ = rolloutsv1alpha1.AddToScheme(s)
	return s
}

func TestReconcileJudgeTemplate_SetsOwnerRef(t *testing.T) {
	scheme := makeRolloutsScheme()
	ad := &agentrollv1alpha1.AgentDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-agent",
			Namespace: "default",
			UID:       "uid-judge",
		},
		Spec: agentrollv1alpha1.AgentDeploymentSpec{
			Evaluation: &agentrollv1alpha1.EvaluationSpec{
				JudgeModel: "claude-haiku-4-5-20251001",
				SecretRef:  "anthropic-creds",
			},
		},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &AgentDeploymentReconciler{Client: fakeClient, Scheme: scheme}

	if err := r.reconcileJudgeTemplate(context.Background(), ad); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	template := &rolloutsv1alpha1.AnalysisTemplate{}
	if err := fakeClient.Get(context.Background(),
		client.ObjectKey{Name: "agent-judge-check", Namespace: "default"}, template); err != nil {
		t.Fatalf("failed to get template: %v", err)
	}
	if len(template.OwnerReferences) == 0 {
		t.Fatal("expected owner references, got none")
	}
	if template.OwnerReferences[0].Name != "test-agent" {
		t.Errorf("owner name: got %q, want %q", template.OwnerReferences[0].Name, "test-agent")
	}
	ownerRef := template.OwnerReferences[0]
	if ownerRef.Controller == nil || !*ownerRef.Controller {
		t.Error("expected Controller=true on owner reference")
	}
	if ownerRef.BlockOwnerDeletion == nil || !*ownerRef.BlockOwnerDeletion {
		t.Error("expected BlockOwnerDeletion=true on owner reference")
	}
	if string(ownerRef.UID) != "uid-judge" {
		t.Errorf("owner UID: got %q, want %q", ownerRef.UID, "uid-judge")
	}
}

func TestReconcileToolCheckTemplate_SetsOwnerRef(t *testing.T) {
	scheme := makeRolloutsScheme()
	ad := &agentrollv1alpha1.AgentDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-agent",
			Namespace: "default",
			UID:       "uid-tool",
		},
		Spec: agentrollv1alpha1.AgentDeploymentSpec{
			AgentMeta: agentrollv1alpha1.AgentMetaSpec{
				ToolDependencies: []agentrollv1alpha1.ToolDependency{
					{Name: "kubectl", Version: "v1.27.0"},
				},
			},
		},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &AgentDeploymentReconciler{Client: fakeClient, Scheme: scheme}

	if err := r.reconcileToolCheckTemplate(context.Background(), ad); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	template := &rolloutsv1alpha1.AnalysisTemplate{}
	if err := fakeClient.Get(context.Background(),
		client.ObjectKey{Name: "agent-tool-check", Namespace: "default"}, template); err != nil {
		t.Fatalf("failed to get template: %v", err)
	}
	if len(template.OwnerReferences) == 0 {
		t.Fatal("expected owner references, got none")
	}
	if template.OwnerReferences[0].Name != "test-agent" {
		t.Errorf("owner name: got %q, want %q", template.OwnerReferences[0].Name, "test-agent")
	}
	ownerRef := template.OwnerReferences[0]
	if ownerRef.Controller == nil || !*ownerRef.Controller {
		t.Error("expected Controller=true on owner reference")
	}
	if ownerRef.BlockOwnerDeletion == nil || !*ownerRef.BlockOwnerDeletion {
		t.Error("expected BlockOwnerDeletion=true on owner reference")
	}
	if string(ownerRef.UID) != "uid-tool" {
		t.Errorf("owner UID: got %q, want %q", ownerRef.UID, "uid-tool")
	}
}

func TestReconcileMemoryCheckTemplate_SetsOwnerRef(t *testing.T) {
	scheme := makeRolloutsScheme()
	ad := &agentrollv1alpha1.AgentDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-agent",
			Namespace: "default",
			UID:       "uid-memory",
		},
		Spec: agentrollv1alpha1.AgentDeploymentSpec{
			Memory: &agentrollv1alpha1.MemorySpec{
				Backend: &agentrollv1alpha1.MemoryBackendSpec{
					Endpoint:  "http://mem0.svc",
					SecretRef: "mem0-creds",
				},
			},
			Evaluation: &agentrollv1alpha1.EvaluationSpec{
				JudgeModel: "claude-haiku-4-5-20251001",
				SecretRef:  "anthropic-creds",
			},
		},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &AgentDeploymentReconciler{Client: fakeClient, Scheme: scheme}

	if err := r.reconcileMemoryCheckTemplate(context.Background(), ad); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	template := &rolloutsv1alpha1.AnalysisTemplate{}
	if err := fakeClient.Get(context.Background(),
		client.ObjectKey{Name: "agent-memory-check", Namespace: "default"}, template); err != nil {
		t.Fatalf("failed to get template: %v", err)
	}
	if len(template.OwnerReferences) == 0 {
		t.Fatal("expected owner references, got none")
	}
	if template.OwnerReferences[0].Name != "test-agent" {
		t.Errorf("owner name: got %q, want %q", template.OwnerReferences[0].Name, "test-agent")
	}
	ownerRef := template.OwnerReferences[0]
	if ownerRef.Controller == nil || !*ownerRef.Controller {
		t.Error("expected Controller=true on owner reference")
	}
	if ownerRef.BlockOwnerDeletion == nil || !*ownerRef.BlockOwnerDeletion {
		t.Error("expected BlockOwnerDeletion=true on owner reference")
	}
	if string(ownerRef.UID) != "uid-memory" {
		t.Errorf("owner UID: got %q, want %q", ownerRef.UID, "uid-memory")
	}
}
