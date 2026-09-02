package formatter

import (
	"encoding/json"

	"github.com/millstonehq/crossplane-plan/pkg/argocd"
	"github.com/millstonehq/crossplane-plan/pkg/differ"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// JSONSchemaVersion identifies the shape of JSONFormatter's output. Bump it
// whenever a field is renamed, removed, or reinterpreted (adding an optional
// field does not require a bump) so downstream consumers can detect a
// breaking change instead of silently misparsing the document.
const JSONSchemaVersion = "1"

// JSONFormatter formats diffs as structured JSON instead of GitHub-flavored
// markdown, for consumers that process the rendered diff programmatically
// (e.g. a policy engine or static analyzer) rather than reading it as a PR
// comment.
type JSONFormatter struct{}

// NewJSONFormatter creates a new JSONFormatter.
func NewJSONFormatter() *JSONFormatter {
	return &JSONFormatter{}
}

// jsonDiff is the envelope for a single-XR diff. It mirrors what
// GitHubFormatter.FormatDiff shows in its header (kind/name/namespace) plus
// the full, untouched differ.DiffResult so no information is flattened away.
type jsonDiff struct {
	SchemaVersion string             `json:"schemaVersion"`
	Kind          string             `json:"kind,omitempty"`
	Name          string             `json:"name,omitempty"`
	Namespace     string             `json:"namespace,omitempty"`
	Result        *differ.DiffResult `json:"result"`
}

// FormatDiff formats a single diff result as JSON.
//
// xr is used only for the kind/name/namespace header fields, matching how
// GitHubFormatter.FormatDiff uses it; result.XR (set by Calculator) is
// serialized in full as part of "result".
func (f *JSONFormatter) FormatDiff(xr *unstructured.Unstructured, result *differ.DiffResult) string {
	doc := jsonDiff{
		SchemaVersion: JSONSchemaVersion,
		Result:        result,
	}
	if xr != nil {
		doc.Kind = xr.GetKind()
		doc.Name = xr.GetName()
		doc.Namespace = xr.GetNamespace()
	}
	return marshal(doc)
}

// jsonMultiDiff is the envelope for a batch of diffs, mirroring
// GitHubFormatter.FormatMultipleDiffs.
type jsonMultiDiff struct {
	SchemaVersion string                        `json:"schemaVersion"`
	Results       map[string]*differ.DiffResult `json:"results"`
	ArgoCD        *argocd.AppDiff               `json:"argocd,omitempty"`
}

// FormatMultipleDiffs formats diff results for multiple XRs, keyed by
// resource name (the same keys XRWatcher uses, including the "DELETED-"
// prefix for deletions), plus an optional ArgoCD Application diff, as a
// single JSON document.
//
// Results is serialized as a JSON object (not an array) because
// encoding/json sorts map[string]V keys when marshaling, which gives
// deterministic output for free without any extra sorting here.
func (f *JSONFormatter) FormatMultipleDiffs(results map[string]*differ.DiffResult, argocdDiff *argocd.AppDiff) string {
	doc := jsonMultiDiff{
		SchemaVersion: JSONSchemaVersion,
		Results:       results,
		ArgoCD:        argocdDiff,
	}
	return marshal(doc)
}

// marshal serializes v as indented JSON. None of the types reachable from
// jsonDiff/jsonMultiDiff contain channels, funcs, or cycles, so
// json.Marshal is not expected to fail here; if it ever does, we still
// return a valid JSON document (rather than an empty string or a panic) so
// callers parsing our output as JSON get a clear error instead of a parse
// failure.
func marshal(v interface{}) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		errDoc, _ := json.Marshal(map[string]string{
			"schemaVersion": JSONSchemaVersion,
			"error":         err.Error(),
		})
		return string(errDoc)
	}
	return string(b)
}
