package formatter

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/millstonehq/crossplane-plan/pkg/argocd"
	"github.com/millstonehq/crossplane-plan/pkg/differ"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestJSONFormatter_FormatDiff_NoChanges(t *testing.T) {
	formatter := NewJSONFormatter()

	xr := &unstructured.Unstructured{}
	xr.SetKind("XGitHubRepository")
	xr.SetName("mill")

	result := &differ.DiffResult{
		XR:         xr,
		RawDiff:    "",
		HasChanges: false,
		Summary:    "No changes detected for XGitHubRepository/mill",
	}

	output := formatter.FormatDiff(xr, result)

	var doc jsonDiff
	if err := json.Unmarshal([]byte(output), &doc); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput:\n%s", err, output)
	}

	if doc.SchemaVersion != JSONSchemaVersion {
		t.Errorf("SchemaVersion = %q, want %q", doc.SchemaVersion, JSONSchemaVersion)
	}
	if doc.Kind != "XGitHubRepository" {
		t.Errorf("Kind = %q, want XGitHubRepository", doc.Kind)
	}
	if doc.Name != "mill" {
		t.Errorf("Name = %q, want mill", doc.Name)
	}
	if doc.Result == nil {
		t.Fatal("Result is nil")
	}
	if doc.Result.HasChanges {
		t.Error("Result.HasChanges = true, want false")
	}
	if doc.Result.Summary != result.Summary {
		t.Errorf("Result.Summary = %q, want %q", doc.Result.Summary, result.Summary)
	}
}

func TestJSONFormatter_FormatDiff_WithNamespace(t *testing.T) {
	formatter := NewJSONFormatter()

	xr := &unstructured.Unstructured{}
	xr.SetKind("XGitHubRepository")
	xr.SetName("mill")
	xr.SetNamespace("millstone-prod")

	result := &differ.DiffResult{XR: xr, Summary: "No changes"}

	var doc jsonDiff
	if err := json.Unmarshal([]byte(formatter.FormatDiff(xr, result)), &doc); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}

	if doc.Namespace != "millstone-prod" {
		t.Errorf("Namespace = %q, want millstone-prod", doc.Namespace)
	}
}

// TestJSONFormatter_FormatDiff_NoLossyFlattening builds a DiffResult with
// nested ManagedResourceState/StrippedField/FieldComparison data and asserts
// every field survives a marshal/unmarshal round trip untouched.
func TestJSONFormatter_FormatDiff_NoLossyFlattening(t *testing.T) {
	formatter := NewJSONFormatter()

	xr := &unstructured.Unstructured{}
	xr.SetKind("XGitHubRepository")
	xr.SetName("pr-123-mill")

	mr := &unstructured.Unstructured{}
	mr.SetKind("Repository")
	mr.SetName("mill")

	result := &differ.DiffResult{
		XR:         xr,
		RawDiff:    "+ added line\n- removed line",
		HasChanges: true,
		Summary:    "Changes detected for XGitHubRepository/pr-123-mill: +1 -1 lines",
		ManagedResources: []differ.ManagedResourceState{
			{
				Resource:           mr,
				ManagementPolicies: []string{"Observe"},
				IsReadOnly:         true,
				SpecForProvider:    map[string]interface{}{"visibility": "private"},
				StatusAtProvider:   map[string]interface{}{"visibility": "public"},
				HasAtProvider:      true,
				IsReady:            true,
				DeclaredVsActual: map[string]differ.FieldComparison{
					"visibility": {Path: "visibility", Declared: "private", Actual: "public"},
				},
			},
		},
		StrippedFields: []differ.StrippedField{
			{Path: "spec.managementPolicies", Reason: "PR previews forced to read-only mode for safety"},
			{Path: "metadata.annotations", Reason: "ArgoCD-managed tracking metadata"},
		},
	}

	output := formatter.FormatDiff(xr, result)

	var doc jsonDiff
	if err := json.Unmarshal([]byte(output), &doc); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput:\n%s", err, output)
	}

	if doc.Result.RawDiff != result.RawDiff {
		t.Errorf("RawDiff = %q, want %q", doc.Result.RawDiff, result.RawDiff)
	}

	if len(doc.Result.ManagedResources) != 1 {
		t.Fatalf("len(ManagedResources) = %d, want 1", len(doc.Result.ManagedResources))
	}
	gotMR := doc.Result.ManagedResources[0]
	if gotMR.Resource == nil || gotMR.Resource.GetName() != "mill" || gotMR.Resource.GetKind() != "Repository" {
		t.Errorf("ManagedResources[0].Resource = %+v, want Repository/mill", gotMR.Resource)
	}
	if !gotMR.IsReadOnly {
		t.Error("ManagedResources[0].IsReadOnly = false, want true")
	}
	if len(gotMR.ManagementPolicies) != 1 || gotMR.ManagementPolicies[0] != "Observe" {
		t.Errorf("ManagedResources[0].ManagementPolicies = %v, want [Observe]", gotMR.ManagementPolicies)
	}
	cmp, ok := gotMR.DeclaredVsActual["visibility"]
	if !ok {
		t.Fatal("ManagedResources[0].DeclaredVsActual missing \"visibility\" key")
	}
	if cmp.Declared != "private" || cmp.Actual != "public" {
		t.Errorf("DeclaredVsActual[visibility] = %+v, want {Declared: private, Actual: public}", cmp)
	}

	if len(doc.Result.StrippedFields) != 2 {
		t.Fatalf("len(StrippedFields) = %d, want 2", len(doc.Result.StrippedFields))
	}
	if doc.Result.StrippedFields[0].Path != "spec.managementPolicies" {
		t.Errorf("StrippedFields[0].Path = %q, want spec.managementPolicies", doc.Result.StrippedFields[0].Path)
	}
	if doc.Result.StrippedFields[1].Reason != "ArgoCD-managed tracking metadata" {
		t.Errorf("StrippedFields[1].Reason = %q, want ArgoCD-managed tracking metadata", doc.Result.StrippedFields[1].Reason)
	}
}

func TestJSONFormatter_FormatDiff_Deterministic(t *testing.T) {
	formatter := NewJSONFormatter()

	xr := &unstructured.Unstructured{}
	xr.SetKind("XGitHubRepository")
	xr.SetName("mill")

	result := &differ.DiffResult{
		XR: xr,
		ManagedResources: []differ.ManagedResourceState{
			{
				DeclaredVsActual: map[string]differ.FieldComparison{
					"zeta":  {Path: "zeta", Declared: 1, Actual: 2},
					"alpha": {Path: "alpha", Declared: 1, Actual: 2},
					"mu":    {Path: "mu", Declared: 1, Actual: 2},
				},
			},
		},
	}

	first := formatter.FormatDiff(xr, result)
	second := formatter.FormatDiff(xr, result)

	if first != second {
		t.Errorf("FormatDiff() is not deterministic across repeated calls:\nfirst:\n%s\nsecond:\n%s", first, second)
	}

	// DeclaredVsActual is a map; encoding/json sorts string map keys, so
	// "alpha" must appear before "mu" and "mu" before "zeta" in the output.
	iAlpha := strings.Index(first, `"alpha"`)
	iMu := strings.Index(first, `"mu"`)
	iZeta := strings.Index(first, `"zeta"`)
	if iAlpha < 0 || iMu < 0 || iZeta < 0 {
		t.Fatalf("expected all three keys present in output:\n%s", first)
	}
	if !(iAlpha < iMu && iMu < iZeta) {
		t.Errorf("map keys not in sorted order: alpha@%d mu@%d zeta@%d", iAlpha, iMu, iZeta)
	}
}

func TestJSONFormatter_FormatMultipleDiffs_SortedKeys(t *testing.T) {
	formatter := NewJSONFormatter()

	results := map[string]*differ.DiffResult{
		"zebra":       {Summary: "z"},
		"apple":       {Summary: "a"},
		"DELETED-mid": {Summary: "m"},
	}

	output := formatter.FormatMultipleDiffs(results, nil)

	var doc jsonMultiDiff
	if err := json.Unmarshal([]byte(output), &doc); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput:\n%s", err, output)
	}
	if len(doc.Results) != 3 {
		t.Fatalf("len(Results) = %d, want 3", len(doc.Results))
	}
	if doc.Results["apple"].Summary != "a" || doc.Results["zebra"].Summary != "z" || doc.Results["DELETED-mid"].Summary != "m" {
		t.Errorf("Results content mismatch: %+v", doc.Results)
	}

	// Verify the raw string is deterministically key-sorted, not just that
	// the parsed map round-trips correctly.
	iApple := strings.Index(output, `"apple"`)
	iDeleted := strings.Index(output, `"DELETED-mid"`)
	iZebra := strings.Index(output, `"zebra"`)
	if iApple < 0 || iDeleted < 0 || iZebra < 0 {
		t.Fatalf("expected all three keys present in output:\n%s", output)
	}
	// ASCII order: "DELETED-mid" < "apple" < "zebra" (uppercase sorts first)
	if !(iDeleted < iApple && iApple < iZebra) {
		t.Errorf("result keys not in sorted order: DELETED-mid@%d apple@%d zebra@%d", iDeleted, iApple, iZebra)
	}
}

func TestJSONFormatter_FormatMultipleDiffs_WithArgoCD(t *testing.T) {
	formatter := NewJSONFormatter()

	results := map[string]*differ.DiffResult{
		"mill": {HasChanges: true, Summary: "Changes: +1 lines"},
	}

	diff := &argocd.AppDiff{
		Additions: []argocd.ResourceChange{
			{GVK: schema.GroupVersionKind{Group: "platform.millstone.tech", Version: "v1alpha1", Kind: "XGitHubRepository"}, Name: "new-repo"},
		},
		Deletions: []argocd.ResourceDeletion{
			{GVK: schema.GroupVersionKind{Group: "platform.millstone.tech", Version: "v1alpha1", Kind: "XGitHubRepository"}, Name: "old-repo"},
		},
		RawDiff: "diff content",
	}

	output := formatter.FormatMultipleDiffs(results, diff)

	var doc jsonMultiDiff
	if err := json.Unmarshal([]byte(output), &doc); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput:\n%s", err, output)
	}
	if doc.ArgoCD == nil {
		t.Fatal("ArgoCD is nil")
	}
	if len(doc.ArgoCD.Additions) != 1 || doc.ArgoCD.Additions[0].Name != "new-repo" {
		t.Errorf("ArgoCD.Additions = %+v, want one entry named new-repo", doc.ArgoCD.Additions)
	}
	if len(doc.ArgoCD.Deletions) != 1 || doc.ArgoCD.Deletions[0].Name != "old-repo" {
		t.Errorf("ArgoCD.Deletions = %+v, want one entry named old-repo", doc.ArgoCD.Deletions)
	}
}

func TestJSONFormatter_FormatMultipleDiffs_ArgoCDOmittedWhenNil(t *testing.T) {
	formatter := NewJSONFormatter()

	output := formatter.FormatMultipleDiffs(map[string]*differ.DiffResult{}, nil)

	if strings.Contains(output, `"argocd"`) {
		t.Errorf("expected \"argocd\" key to be omitted when argocdDiff is nil, got:\n%s", output)
	}
}

func TestJSONFormatter_ImplementsFormatter(t *testing.T) {
	var _ Formatter = NewJSONFormatter()
}
