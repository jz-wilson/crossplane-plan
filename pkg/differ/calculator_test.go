package differ

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	"github.com/millstonehq/crossplane-plan/pkg/config"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/rest"
)

func TestCalculator_generateSummary_NoChanges(t *testing.T) {
	calc := &Calculator{}

	xr := &unstructured.Unstructured{}
	xr.SetKind("XGitHubRepository")
	xr.SetName("mill")

	summary := calc.generateSummary(xr, "", false)

	expected := "No changes detected for XGitHubRepository/mill"
	if summary != expected {
		t.Errorf("generateSummary() = %q, want %q", summary, expected)
	}
}

func TestCalculator_generateSummary_WithChanges(t *testing.T) {
	calc := &Calculator{}

	xr := &unstructured.Unstructured{}
	xr.SetKind("XGitHubRepository")
	xr.SetName("pr-123-mill")

	diff := `+ added line 1
+ added line 2
- removed line 1
  context line`

	summary := calc.generateSummary(xr, diff, true)

	if !strings.Contains(summary, "XGitHubRepository/pr-123-mill") {
		t.Error("Summary missing resource name")
	}

	if !strings.Contains(summary, "+2") {
		t.Error("Summary missing addition count")
	}

	if !strings.Contains(summary, "-1") {
		t.Error("Summary missing deletion count")
	}
}

func TestCalculator_generateSummary_EmptyDiff(t *testing.T) {
	calc := &Calculator{}

	xr := &unstructured.Unstructured{}
	xr.SetKind("XGitHubRepository")
	xr.SetName("test")

	// Empty diff with hasChanges=true (edge case)
	summary := calc.generateSummary(xr, "", true)

	expected := "Changes detected for XGitHubRepository/test: +0 -0 lines"
	if summary != expected {
		t.Errorf("generateSummary() = %q, want %q", summary, expected)
	}
}

func TestCalculator_generateSummary_OnlyAdditions(t *testing.T) {
	calc := &Calculator{}

	xr := &unstructured.Unstructured{}
	xr.SetKind("XCrossplaneProviderRepository")
	xr.SetName("provider-github")

	diff := `+ line 1
+ line 2
+ line 3
  context`

	summary := calc.generateSummary(xr, diff, true)

	if !strings.Contains(summary, "+3") {
		t.Error("Summary should show +3 additions")
	}

	if !strings.Contains(summary, "-0") {
		t.Error("Summary should show -0 deletions")
	}
}

func TestCalculator_generateSummary_OnlyDeletions(t *testing.T) {
	calc := &Calculator{}

	xr := &unstructured.Unstructured{}
	xr.SetKind("XGitHubRepository")
	xr.SetName("old-repo")

	diff := `- line 1
- line 2
  context`

	summary := calc.generateSummary(xr, diff, true)

	if !strings.Contains(summary, "+0") {
		t.Error("Summary should show +0 additions")
	}

	if !strings.Contains(summary, "-2") {
		t.Error("Summary should show -2 deletions")
	}
}

func TestNewCalculator(t *testing.T) {
	// Use an empty config (won't actually connect to cluster for this test)
	cfg := &rest.Config{}
	logger := logging.NewNopLogger()

	calc := NewCalculator(cfg, logger)

	if calc == nil {
		t.Fatal("NewCalculator() returned nil")
	}

	if calc.config != cfg {
		t.Error("Calculator config not set correctly")
	}

	if calc.logger == nil {
		t.Error("Calculator logger not set")
	}

	if calc.initialized {
		t.Error("Calculator should not be initialized on creation")
	}
}

func TestCalculator_SetSanitizer(t *testing.T) {
	calc := &Calculator{}

	rules := []config.StripRule{
		{Path: "test.path", Reason: "test"},
	}
	sanitizer := NewSanitizer(rules)

	calc.SetSanitizer(sanitizer)

	if calc.sanitizer == nil {
		t.Fatal("SetSanitizer() did not set sanitizer")
	}

	if calc.sanitizer != sanitizer {
		t.Error("SetSanitizer() did not set the correct sanitizer instance")
	}
}

// TestDiffResult_JSONTags locks in the JSON field names on DiffResult and its
// nested types, since formatter.JSONFormatter depends on them for a stable
// wire format. If this test breaks, formatter.JSONSchemaVersion likely needs
// a bump too.
func TestDiffResult_JSONTags(t *testing.T) {
	xr := &unstructured.Unstructured{}
	xr.SetKind("XGitHubRepository")
	xr.SetName("mill")

	result := &DiffResult{
		XR:         xr,
		RawDiff:    "+ line",
		HasChanges: true,
		Summary:    "1 change",
		ManagedResources: []ManagedResourceState{
			{
				Resource:   xr,
				IsReadOnly: true,
				DeclaredVsActual: map[string]FieldComparison{
					"field": {Path: "field", Declared: "a", Actual: "b"},
				},
			},
		},
		StrippedFields: []StrippedField{
			{Path: "spec.foo", Reason: "noise"},
		},
	}

	b, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	for _, key := range []string{"rawDiff", "hasChanges", "summary", "managedResources", "strippedFields", "xr"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("expected top-level JSON key %q, got keys: %v", key, mapKeys(decoded))
		}
	}

	managedResources, ok := decoded["managedResources"].([]interface{})
	if !ok || len(managedResources) != 1 {
		t.Fatalf("managedResources = %v, want a single-element array", decoded["managedResources"])
	}
	mr, ok := managedResources[0].(map[string]interface{})
	if !ok {
		t.Fatalf("managedResources[0] is not an object: %v", managedResources[0])
	}
	for _, key := range []string{"resource", "isReadOnly", "declaredVsActual"} {
		if _, ok := mr[key]; !ok {
			t.Errorf("expected managedResources[0] key %q, got keys: %v", key, mapKeys(mr))
		}
	}

	strippedFields, ok := decoded["strippedFields"].([]interface{})
	if !ok || len(strippedFields) != 1 {
		t.Fatalf("strippedFields = %v, want a single-element array", decoded["strippedFields"])
	}
	sf, ok := strippedFields[0].(map[string]interface{})
	if !ok {
		t.Fatalf("strippedFields[0] is not an object: %v", strippedFields[0])
	}
	for _, key := range []string{"path", "reason"} {
		if _, ok := sf[key]; !ok {
			t.Errorf("expected strippedFields[0] key %q, got keys: %v", key, mapKeys(sf))
		}
	}
}

func mapKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
