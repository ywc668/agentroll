/*
Copyright 2026 AgentRoll Contributors.
Licensed under the MIT License.
*/

// Package v1alpha1 contains API Schema definitions for the agentroll v1alpha1 API group
// +kubebuilder:object:generate=true
// +groupName=agentroll.dev
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	// GroupVersion is group version used to register these objects
	GroupVersion = schema.GroupVersion{Group: "agentroll.dev", Version: "v1alpha1"}

	// SchemeBuilder is used to add go types to the GroupVersionResource scheme
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	// AddToScheme adds the types in this group-version to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)
