// Package v1alpha1 contains the API schema definitions for the kanary v1alpha1 API group.
//
// +kubebuilder:object:generate=true
// +groupName=kanary.io
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

// GroupVersion is the identifier for this API group/version.
var GroupVersion = schema.GroupVersion{Group: "kanary.io", Version: "v1alpha1"}

// SchemeBuilder is used to register the types with a runtime.Scheme.
var SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

// AddToScheme adds the types in this group-version to the given scheme.
var AddToScheme = SchemeBuilder.AddToScheme
