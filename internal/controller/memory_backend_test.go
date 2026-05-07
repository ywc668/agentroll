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
