package formatter

import (
	"github.com/millstonehq/crossplane-plan/pkg/argocd"
	"github.com/millstonehq/crossplane-plan/pkg/differ"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Formatter renders diff results into an output format. Implementations
// include GitHubFormatter (GitHub-flavored markdown, for posting as a PR
// comment) and JSONFormatter (structured JSON, for programmatic consumers).
type Formatter interface {
	// FormatDiff formats the diff result for a single XR.
	FormatDiff(xr *unstructured.Unstructured, result *differ.DiffResult) string

	// FormatMultipleDiffs formats diff results for one or more XRs, keyed by
	// resource name, plus an optional ArgoCD Application diff. argocdDiff may
	// be nil when ArgoCD integration is disabled or unavailable.
	FormatMultipleDiffs(results map[string]*differ.DiffResult, argocdDiff *argocd.AppDiff) string
}

// Compile-time assertions that both formatters satisfy Formatter.
var (
	_ Formatter = (*GitHubFormatter)(nil)
	_ Formatter = (*JSONFormatter)(nil)
)
