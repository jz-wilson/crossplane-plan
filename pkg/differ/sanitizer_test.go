package differ

import (
	"testing"

	"github.com/millstonehq/crossplane-plan/pkg/config"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestNewSanitizer(t *testing.T) {
	rules := []config.StripRule{
		{Path: "test.path", Reason: "test"},
	}

	sanitizer := NewSanitizer(rules)

	if sanitizer == nil {
		t.Fatal("NewSanitizer() returned nil")
	}

	if len(sanitizer.rules) != 1 {
		t.Errorf("sanitizer.rules length = %d, want 1", len(sanitizer.rules))
	}
}

func TestSanitizer_Sanitize_NoRules(t *testing.T) {
	sanitizer := NewSanitizer([]config.StripRule{})

	xr := &unstructured.Unstructured{}
	xr.SetKind("XGitHubRepository")
	xr.SetName("test")
	xr.Object["spec"] = map[string]interface{}{
		"field": "value",
	}

	result := sanitizer.Sanitize(xr)

	if result == nil {
		t.Fatal("Sanitize() returned nil")
	}

	if result.SanitizedXR == nil {
		t.Fatal("SanitizedXR is nil")
	}

	if len(result.StrippedFields) != 0 {
		t.Errorf("StrippedFields length = %d, want 0", len(result.StrippedFields))
	}

	// Original should be unchanged
	if xr.Object["spec"] == nil {
		t.Error("Original XR was modified")
	}
}

func TestSanitizer_Sanitize_StripByEquals(t *testing.T) {
	rules := []config.StripRule{
		{
			Path:   "spec.managementPolicies",
			Equals: []interface{}{"Observe"},
			Reason: "Preview mode",
		},
	}
	sanitizer := NewSanitizer(rules)

	xr := &unstructured.Unstructured{}
	xr.SetKind("XGitHubRepository")
	xr.SetName("test")
	xr.Object["spec"] = map[string]interface{}{
		"managementPolicies": []interface{}{"Observe"},
		"name":               "test-repo",
	}

	result := sanitizer.Sanitize(xr)

	// Check that managementPolicies was stripped
	spec, _ := result.SanitizedXR.Object["spec"].(map[string]interface{})
	if spec["managementPolicies"] != nil {
		t.Error("managementPolicies should have been stripped")
	}

	// Check that other fields remain
	if spec["name"] != "test-repo" {
		t.Error("Other spec fields should remain")
	}

	// Check stripped fields tracking
	if len(result.StrippedFields) != 1 {
		t.Fatalf("StrippedFields length = %d, want 1", len(result.StrippedFields))
	}

	if result.StrippedFields[0].Path != "spec.managementPolicies" {
		t.Errorf("StrippedFields[0].Path = %s, want spec.managementPolicies", result.StrippedFields[0].Path)
	}
}

func TestSanitizer_Sanitize_DontStripNonMatch(t *testing.T) {
	rules := []config.StripRule{
		{
			Path:   "spec.managementPolicies",
			Equals: []interface{}{"Observe"},
			Reason: "Preview mode",
		},
	}
	sanitizer := NewSanitizer(rules)

	xr := &unstructured.Unstructured{}
	xr.Object = map[string]interface{}{
		"spec": map[string]interface{}{
			"managementPolicies": []interface{}{"Create", "Update"},
		},
	}

	result := sanitizer.Sanitize(xr)

	// Should NOT be stripped because value doesn't match
	spec, _ := result.SanitizedXR.Object["spec"].(map[string]interface{})
	if spec["managementPolicies"] == nil {
		t.Error("managementPolicies should NOT have been stripped (value doesn't match)")
	}

	if len(result.StrippedFields) != 0 {
		t.Errorf("StrippedFields should be empty, got %d", len(result.StrippedFields))
	}
}

func TestSanitizer_Sanitize_StripMatchingAnnotations(t *testing.T) {
	rules := []config.StripRule{
		{
			Path:    "metadata.annotations",
			Pattern: `^argocd\.argoproj\.io/.*`,
			Reason:  "ArgoCD tracking",
		},
	}
	sanitizer := NewSanitizer(rules)

	xr := &unstructured.Unstructured{}
	xr.SetAnnotations(map[string]string{
		"argocd.argoproj.io/tracking-id": "abc123",
		"argocd.argoproj.io/sync-wave":   "1",
		"custom.io/annotation":           "keep-me",
		"millstone.tech/preview-pr":      "123",
	})

	result := sanitizer.Sanitize(xr)

	annotations := result.SanitizedXR.GetAnnotations()

	// ArgoCD annotations should be stripped
	if annotations["argocd.argoproj.io/tracking-id"] != "" {
		t.Error("ArgoCD tracking-id should be stripped")
	}
	if annotations["argocd.argoproj.io/sync-wave"] != "" {
		t.Error("ArgoCD sync-wave should be stripped")
	}

	// Other annotations should remain
	if annotations["custom.io/annotation"] != "keep-me" {
		t.Error("custom.io/annotation should remain")
	}
	if annotations["millstone.tech/preview-pr"] != "123" {
		t.Error("millstone.tech/preview-pr should remain")
	}

	// Check stripped fields tracking
	if len(result.StrippedFields) != 1 {
		t.Fatalf("StrippedFields length = %d, want 1", len(result.StrippedFields))
	}
}

func TestSanitizer_Sanitize_StripMatchingLabels(t *testing.T) {
	rules := []config.StripRule{
		{
			Path:    "metadata.labels",
			Pattern: `^crossplane\.io/.*`,
			Reason:  "Crossplane internal",
		},
	}
	sanitizer := NewSanitizer(rules)

	xr := &unstructured.Unstructured{}
	xr.SetLabels(map[string]string{
		"crossplane.io/composite":  "true",
		"crossplane.io/claim-name": "my-claim",
		"app.kubernetes.io/name":   "test",
		"environment":              "production",
	})

	result := sanitizer.Sanitize(xr)

	labels := result.SanitizedXR.GetLabels()

	// Crossplane labels should be stripped
	if labels["crossplane.io/composite"] != "" {
		t.Error("crossplane.io/composite should be stripped")
	}
	if labels["crossplane.io/claim-name"] != "" {
		t.Error("crossplane.io/claim-name should be stripped")
	}

	// Other labels should remain
	if labels["app.kubernetes.io/name"] != "test" {
		t.Error("app.kubernetes.io/name should remain")
	}
	if labels["environment"] != "production" {
		t.Error("environment should remain")
	}

	// Check stripped fields tracking
	if len(result.StrippedFields) != 1 {
		t.Fatalf("StrippedFields length = %d, want 1", len(result.StrippedFields))
	}
}

func TestSanitizer_Sanitize_InvalidPattern(t *testing.T) {
	rules := []config.StripRule{
		{
			Path:    "metadata.annotations",
			Pattern: `[invalid(regex`,
			Reason:  "Invalid",
		},
	}
	sanitizer := NewSanitizer(rules)

	xr := &unstructured.Unstructured{}
	xr.SetAnnotations(map[string]string{
		"test": "value",
	})

	result := sanitizer.Sanitize(xr)

	// Should handle invalid regex gracefully
	annotations := result.SanitizedXR.GetAnnotations()
	if annotations["test"] != "value" {
		t.Error("Annotations should remain unchanged with invalid pattern")
	}

	if len(result.StrippedFields) != 0 {
		t.Error("Nothing should be stripped with invalid pattern")
	}
}

func TestSanitizer_Sanitize_MultipleRules(t *testing.T) {
	rules := []config.StripRule{
		{
			Path:   "spec.managementPolicies",
			Equals: []interface{}{"Observe"},
			Reason: "Preview mode",
		},
		{
			Path:    "metadata.annotations",
			Pattern: `^argocd\.argoproj\.io/.*`,
			Reason:  "ArgoCD tracking",
		},
		{
			Path:    "metadata.labels",
			Pattern: `^crossplane\.io/.*`,
			Reason:  "Crossplane internal",
		},
	}
	sanitizer := NewSanitizer(rules)

	xr := &unstructured.Unstructured{}
	xr.Object = map[string]interface{}{
		"spec": map[string]interface{}{
			"managementPolicies": []interface{}{"Observe"},
			"name":               "test",
		},
	}
	xr.SetAnnotations(map[string]string{
		"argocd.argoproj.io/tracking-id": "abc",
		"custom/annotation":              "keep",
	})
	xr.SetLabels(map[string]string{
		"crossplane.io/composite": "true",
		"custom/label":            "keep",
	})

	result := sanitizer.Sanitize(xr)

	// Check spec
	spec, _ := result.SanitizedXR.Object["spec"].(map[string]interface{})
	if spec["managementPolicies"] != nil {
		t.Error("managementPolicies should be stripped")
	}

	// Check annotations
	annotations := result.SanitizedXR.GetAnnotations()
	if annotations["argocd.argoproj.io/tracking-id"] != "" {
		t.Error("ArgoCD annotation should be stripped")
	}
	if annotations["custom/annotation"] != "keep" {
		t.Error("Custom annotation should remain")
	}

	// Check labels
	labels := result.SanitizedXR.GetLabels()
	if labels["crossplane.io/composite"] != "" {
		t.Error("Crossplane label should be stripped")
	}
	if labels["custom/label"] != "keep" {
		t.Error("Custom label should remain")
	}

	// Should have 3 stripped fields
	if len(result.StrippedFields) != 3 {
		t.Errorf("StrippedFields length = %d, want 3", len(result.StrippedFields))
	}
}

func TestSanitizer_valuesEqual(t *testing.T) {
	sanitizer := &Sanitizer{}

	tests := []struct {
		name  string
		a     interface{}
		b     interface{}
		equal bool
	}{
		{
			name:  "equal string slices",
			a:     []interface{}{"Observe"},
			b:     []interface{}{"Observe"},
			equal: true,
		},
		{
			name:  "unequal string slices",
			a:     []interface{}{"Observe"},
			b:     []interface{}{"Create"},
			equal: false,
		},
		{
			name:  "different length slices",
			a:     []interface{}{"Observe"},
			b:     []interface{}{"Observe", "Create"},
			equal: false,
		},
		{
			name:  "equal strings",
			a:     "test",
			b:     "test",
			equal: true,
		},
		{
			name:  "unequal strings",
			a:     "test",
			b:     "other",
			equal: false,
		},
		{
			name:  "equal numbers",
			a:     42,
			b:     42,
			equal: true,
		},
		{
			name:  "unequal numbers",
			a:     42,
			b:     43,
			equal: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sanitizer.valuesEqual(tt.a, tt.b)
			if result != tt.equal {
				t.Errorf("valuesEqual() = %v, want %v", result, tt.equal)
			}
		})
	}
}

func TestSanitizer_Sanitize_FieldNotFound(t *testing.T) {
	rules := []config.StripRule{
		{
			Path:   "spec.nonexistent",
			Equals: "value",
			Reason: "Test",
		},
	}
	sanitizer := NewSanitizer(rules)

	xr := &unstructured.Unstructured{}
	xr.Object = map[string]interface{}{
		"spec": map[string]interface{}{
			"other": "field",
		},
	}

	result := sanitizer.Sanitize(xr)

	// Should handle gracefully
	if len(result.StrippedFields) != 0 {
		t.Error("Nothing should be stripped for nonexistent field")
	}
}

func TestSanitizer_Sanitize_NoAnnotations(t *testing.T) {
	rules := []config.StripRule{
		{
			Path:    "metadata.annotations",
			Pattern: `^test/.*`,
			Reason:  "Test",
		},
	}
	sanitizer := NewSanitizer(rules)

	xr := &unstructured.Unstructured{}
	// No annotations set

	result := sanitizer.Sanitize(xr)

	// Should handle gracefully
	if len(result.StrippedFields) != 0 {
		t.Error("Nothing should be stripped when no annotations exist")
	}
}

func TestSanitizer_Sanitize_NoLabels(t *testing.T) {
	rules := []config.StripRule{
		{
			Path:    "metadata.labels",
			Pattern: `^test/.*`,
			Reason:  "Test",
		},
	}
	sanitizer := NewSanitizer(rules)

	xr := &unstructured.Unstructured{}
	// No labels set

	result := sanitizer.Sanitize(xr)

	// Should handle gracefully
	if len(result.StrippedFields) != 0 {
		t.Error("Nothing should be stripped when no labels exist")
	}
}
