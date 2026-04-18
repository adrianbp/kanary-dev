package nginx_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kanaryv1alpha1 "github.com/adrianbp/kanary-dev/api/v1alpha1"
	"github.com/adrianbp/kanary-dev/internal/traffic/nginx"
)

// TestSiblingIngress is a table-driven test (Go in Action, 2nd Ed., Ch. 8)
// that validates the shape of the generated sibling Ingress for varied inputs.
func TestSiblingIngress(t *testing.T) {
	t.Parallel()

	canary := &kanaryv1alpha1.Canary{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout-api", Namespace: "prod"},
		Spec: kanaryv1alpha1.CanarySpec{
			TargetRef: kanaryv1alpha1.TargetRef{
				Kind: "Deployment", Name: "checkout-api", APIVersion: "apps/v1",
			},
			TrafficProvider: kanaryv1alpha1.TrafficProvider{
				Type:       kanaryv1alpha1.TrafficProviderNginx,
				IngressRef: &kanaryv1alpha1.LocalObjectReference{Name: "checkout-api"},
			},
		},
	}

	baseStable := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout-api", Namespace: "prod"},
		Spec: networkingv1.IngressSpec{
			Rules: []networkingv1.IngressRule{
				{
					IngressRuleValue: networkingv1.IngressRuleValue{
						HTTP: &networkingv1.HTTPIngressRuleValue{
							Paths: []networkingv1.HTTPIngressPath{
								{
									Path: "/",
									Backend: networkingv1.IngressBackend{
										Service: &networkingv1.IngressServiceBackend{
											Name: "checkout-api",
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	tests := []struct {
		name       string
		weight     int32
		wantCanary string
		wantWeight string
		wantSvc    string
	}{
		{"zero weight", 0, "true", "0", "checkout-api-canary"},
		{"10%", 10, "true", "10", "checkout-api-canary"},
		{"50%", 50, "true", "50", "checkout-api-canary"},
		{"100%", 100, "true", "100", "checkout-api-canary"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := nginx.SiblingIngress(canary, baseStable, tc.weight)

			require.Equal(t, "checkout-api-kanary", got.Name, "sibling name")
			require.Equal(t, "prod", got.Namespace, "namespace preserved")
			require.Equal(t, tc.wantCanary, got.Annotations[nginx.AnnotationCanary], "canary annotation")
			require.Equal(t, tc.wantWeight, got.Annotations[nginx.AnnotationCanaryWeight], "canary-weight annotation")

			require.Len(t, got.Spec.Rules, 1)
			require.Len(t, got.Spec.Rules[0].HTTP.Paths, 1)
			require.Equal(t, tc.wantSvc, got.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Name, "backend rewritten to canary service")

			require.Equal(t, "true", got.Labels[kanaryv1alpha1.LabelManaged])
			require.Equal(t, canary.Name, got.Labels[kanaryv1alpha1.LabelCanary])
		})
	}
}

// TestSiblingIngress_NoHTTPRule ensures we don't panic on rules without an HTTP block.
func TestSiblingIngress_NoHTTPRule(t *testing.T) {
	t.Parallel()

	canary := &kanaryv1alpha1.Canary{
		ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "ns"},
		Spec: kanaryv1alpha1.CanarySpec{
			TargetRef: kanaryv1alpha1.TargetRef{Name: "x"},
			TrafficProvider: kanaryv1alpha1.TrafficProvider{
				Type:       kanaryv1alpha1.TrafficProviderNginx,
				IngressRef: &kanaryv1alpha1.LocalObjectReference{Name: "x"},
			},
		},
	}
	stable := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "ns"},
		Spec: networkingv1.IngressSpec{
			Rules: []networkingv1.IngressRule{{}}, // HTTP nil
		},
	}

	require.NotPanics(t, func() {
		_ = nginx.SiblingIngress(canary, stable, 25)
	})
}
