package differ

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/crossplane-contrib/crossplane-diff/cmd/diff/client/core"
	xp "github.com/crossplane-contrib/crossplane-diff/cmd/diff/client/crossplane"
	k8 "github.com/crossplane-contrib/crossplane-diff/cmd/diff/client/kubernetes"
	"github.com/crossplane-contrib/crossplane-diff/cmd/diff/diffprocessor"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
)

// ManagedResourceState captures the state of a managed resource
//
// JSON tags define the stable wire format consumed by formatter.JSONFormatter;
// keep them in sync with any field additions/renames (see JSONFormatter's
// schema version doc comment).
type ManagedResourceState struct {
	// Resource is the managed resource
	Resource *unstructured.Unstructured `json:"resource"`

	// ManagementPolicies from the resource spec
	ManagementPolicies []string `json:"managementPolicies,omitempty"`

	// IsReadOnly indicates if resource has Observe-only management policy
	IsReadOnly bool `json:"isReadOnly"`

	// SpecForProvider is the desired state from spec.forProvider
	SpecForProvider map[string]interface{} `json:"specForProvider,omitempty"`

	// StatusAtProvider is the observed state from status.atProvider
	StatusAtProvider map[string]interface{} `json:"statusAtProvider,omitempty"`

	// HasAtProvider indicates if status.atProvider exists and is populated
	HasAtProvider bool `json:"hasAtProvider"`

	// IsReady indicates if the resource Ready condition is True
	IsReady bool `json:"isReady"`

	// DeclaredVsActual contains fields that differ between spec and status
	DeclaredVsActual map[string]FieldComparison `json:"declaredVsActual,omitempty"`
}

// FieldComparison represents a difference between declared and actual state
type FieldComparison struct {
	Path     string      `json:"path"`
	Declared interface{} `json:"declared"`
	Actual   interface{} `json:"actual"`
}

// DiffResult represents the structured diff output
//
// JSON tags define the stable wire format consumed by formatter.JSONFormatter;
// keep them in sync with any field additions/renames (see JSONFormatter's
// schema version doc comment).
type DiffResult struct {
	// XR is the Composite Resource being diffed
	XR *unstructured.Unstructured `json:"xr,omitempty"`

	// RawDiff is the raw diff output from crossplane-diff
	RawDiff string `json:"rawDiff"`

	// HasChanges indicates if there are any changes
	HasChanges bool `json:"hasChanges"`

	// Summary provides a high-level summary of changes
	Summary string `json:"summary"`

	// ManagedResources contains state information for managed resources
	ManagedResources []ManagedResourceState `json:"managedResources,omitempty"`

	// StrippedFields tracks fields that were removed before diff for transparency
	StrippedFields []StrippedField `json:"strippedFields,omitempty"`
}

// StrippedField represents a field that was stripped before diff
type StrippedField struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// Calculator uses crossplane-diff library to calculate diffs
type Calculator struct {
	config      *rest.Config
	logger      logging.Logger
	k8sClients  k8.Clients
	xpClients   xp.Clients
	processor   diffprocessor.DiffProcessor
	sanitizer   *Sanitizer
	initialized bool
}

// NewCalculator creates a new Calculator
func NewCalculator(config *rest.Config, logger logging.Logger) *Calculator {
	return &Calculator{
		config: config,
		logger: logger,
	}
}

// SetSanitizer sets the sanitizer for stripping noise fields
func (c *Calculator) SetSanitizer(sanitizer *Sanitizer) {
	c.sanitizer = sanitizer
}

// Initialize sets up the Kubernetes and Crossplane clients
func (c *Calculator) Initialize(ctx context.Context) error {
	if c.initialized {
		return nil
	}

	// Create core clients
	coreClients, err := core.NewClients(c.config)
	if err != nil {
		return fmt.Errorf("failed to create core clients: %w", err)
	}

	// Create type converter
	tc := k8.NewTypeConverter(coreClients, c.logger)

	// Create K8s clients
	c.k8sClients = k8.Clients{
		Type:     tc,
		Apply:    k8.NewApplyClient(coreClients, tc, c.logger),
		Resource: k8.NewResourceClient(coreClients, tc, c.logger),
		Schema:   k8.NewSchemaClient(coreClients, tc, c.logger),
	}

	// Create Crossplane clients
	defClient := xp.NewDefinitionClient(c.k8sClients.Resource, c.logger)
	c.xpClients = xp.Clients{
		Definition:   defClient,
		Composition:  xp.NewCompositionClient(c.k8sClients.Resource, defClient, c.logger),
		Environment:  xp.NewEnvironmentClient(c.k8sClients.Resource, c.logger),
		Function:     xp.NewFunctionClient(c.k8sClients.Resource, c.logger),
		ResourceTree: xp.NewResourceTreeClient(coreClients.Tree, c.logger),
	}

	// Initialize Crossplane clients
	if err := c.xpClients.Initialize(ctx, c.logger); err != nil {
		return fmt.Errorf("failed to initialize crossplane clients: %w", err)
	}

	// Create diff processor
	c.processor = diffprocessor.NewDiffProcessor(
		c.k8sClients,
		c.xpClients,
		diffprocessor.WithLogger(c.logger),
		diffprocessor.WithNamespace("default"),
		diffprocessor.WithColorize(false), // No colors for structured output
		diffprocessor.WithCompact(false),
		diffprocessor.WithMaxNestedDepth(10), // Default depth limit for nested XRs
	)

	// Initialize processor
	if err := c.processor.Initialize(ctx); err != nil {
		return fmt.Errorf("failed to initialize diff processor: %w", err)
	}

	c.initialized = true
	return nil
}

// CalculateDiff calculates the diff for an XR using crossplane-diff library
func (c *Calculator) CalculateDiff(ctx context.Context, xr *unstructured.Unstructured) (*DiffResult, error) {
	if !c.initialized {
		if err := c.Initialize(ctx); err != nil {
			return nil, fmt.Errorf("failed to initialize calculator: %w", err)
		}
	}

	// Sanitize XR if sanitizer is configured
	var strippedFields []StrippedField
	xrForDiff := xr
	if c.sanitizer != nil {
		sanitizeResult := c.sanitizer.Sanitize(xr)
		xrForDiff = sanitizeResult.SanitizedXR
		strippedFields = sanitizeResult.StrippedFields
	}

	// Use a buffer to capture diff output
	var buf bytes.Buffer

	// Perform diff - PerformDiff writes to io.Writer
	resources := []*unstructured.Unstructured{xrForDiff}
	err := c.processor.PerformDiff(ctx, &buf, resources, c.xpClients.Composition.FindMatchingComposition)

	diffOutput := buf.String()
	hasChanges := len(strings.TrimSpace(diffOutput)) > 0

	if err != nil {
		return nil, fmt.Errorf("failed to calculate diff: %w", err)
	}

	result := &DiffResult{
		XR:             xr,
		RawDiff:        diffOutput,
		HasChanges:     hasChanges,
		Summary:        c.generateSummary(xr, diffOutput, hasChanges),
		StrippedFields: strippedFields,
	}

	// Fetch and analyze managed resources
	managedResources, err := c.fetchManagedResources(ctx, xr)
	if err != nil {
		c.logger.Info("Failed to fetch managed resources", "error", err)
		// Non-fatal: continue with cluster diff only
	} else {
		result.ManagedResources = managedResources
	}

	return result, nil
}

// generateSummary creates a high-level summary of the diff
func (c *Calculator) generateSummary(xr *unstructured.Unstructured, diff string, hasChanges bool) string {
	if !hasChanges {
		return fmt.Sprintf("No changes detected for %s/%s", xr.GetKind(), xr.GetName())
	}

	// Count additions and deletions
	additions := 0
	deletions := 0
	lines := strings.Split(diff, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if strings.HasPrefix(line, "+") {
			additions++
		} else if strings.HasPrefix(line, "-") {
			deletions++
		}
	}

	return fmt.Sprintf("Changes detected for %s/%s: +%d -%d lines",
		xr.GetKind(), xr.GetName(), additions, deletions)
}

// fetchManagedResources fetches managed resources for an XR and analyzes their state
func (c *Calculator) fetchManagedResources(ctx context.Context, xr *unstructured.Unstructured) ([]ManagedResourceState, error) {
	// Get resourceRefs from XR spec
	resourceRefs, found, err := unstructured.NestedSlice(xr.Object, "spec", "resourceRefs")
	if err != nil || !found || len(resourceRefs) == 0 {
		return nil, fmt.Errorf("no resourceRefs found in XR")
	}

	var managedResources []ManagedResourceState

	for _, ref := range resourceRefs {
		refMap, ok := ref.(map[string]interface{})
		if !ok {
			continue
		}

		apiVersion, _, _ := unstructured.NestedString(refMap, "apiVersion")
		kind, _, _ := unstructured.NestedString(refMap, "kind")
		name, _, _ := unstructured.NestedString(refMap, "name")

		if apiVersion == "" || kind == "" || name == "" {
			continue
		}

		// Parse GV from apiVersion
		gv, err := schema.ParseGroupVersion(apiVersion)
		if err != nil {
			c.logger.Info("Failed to parse apiVersion", "apiVersion", apiVersion, "error", err)
			continue
		}

		// Construct GVK
		gvk := schema.GroupVersionKind{
			Group:   gv.Group,
			Version: gv.Version,
			Kind:    kind,
		}

		// Fetch the managed resource (managed resources are cluster-scoped)
		mr, err := c.k8sClients.Resource.GetResource(ctx, gvk, "", name)
		if err != nil {
			c.logger.Info("Failed to fetch managed resource", "name", name, "gvk", gvk.String(), "error", err)
			continue
		}

		// Analyze the managed resource
		state := c.analyzeManagedResource(mr)
		managedResources = append(managedResources, state)
	}

	return managedResources, nil
}

// analyzeManagedResource extracts and compares state from a managed resource
func (c *Calculator) analyzeManagedResource(mr *unstructured.Unstructured) ManagedResourceState {
	state := ManagedResourceState{
		Resource:         mr,
		DeclaredVsActual: make(map[string]FieldComparison),
	}

	// Extract managementPolicies
	policies, found, _ := unstructured.NestedStringSlice(mr.Object, "spec", "managementPolicies")
	if found {
		state.ManagementPolicies = policies
		// Check if it's read-only (exactly ["Observe"])
		state.IsReadOnly = len(policies) == 1 && policies[0] == "Observe"
	}

	// Extract spec.forProvider
	forProvider, found, _ := unstructured.NestedMap(mr.Object, "spec", "forProvider")
	if found {
		state.SpecForProvider = forProvider
	}

	// Extract status.atProvider
	atProvider, found, _ := unstructured.NestedMap(mr.Object, "status", "atProvider")
	if found && len(atProvider) > 0 {
		state.StatusAtProvider = atProvider
		state.HasAtProvider = true
	}

	// Check Ready condition
	conditions, found, _ := unstructured.NestedSlice(mr.Object, "status", "conditions")
	if found {
		for _, cond := range conditions {
			condMap, ok := cond.(map[string]interface{})
			if !ok {
				continue
			}
			condType, _, _ := unstructured.NestedString(condMap, "type")
			condStatus, _, _ := unstructured.NestedString(condMap, "status")
			if condType == "Ready" && condStatus == "True" {
				state.IsReady = true
				break
			}
		}
	}

	// Compare spec.forProvider vs status.atProvider
	if state.HasAtProvider && state.SpecForProvider != nil {
		state.DeclaredVsActual = c.compareFields(state.SpecForProvider, state.StatusAtProvider)
	}

	return state
}

// compareFields compares two maps and returns differences
func (c *Calculator) compareFields(declared, actual map[string]interface{}) map[string]FieldComparison {
	differences := make(map[string]FieldComparison)

	// Check all fields in declared state
	for key, declaredValue := range declared {
		actualValue, exists := actual[key]

		// Skip if actual doesn't have this field
		if !exists {
			continue
		}

		// Compare values (simple comparison, could be enhanced)
		if !c.valuesEqual(declaredValue, actualValue) {
			differences[key] = FieldComparison{
				Path:     key,
				Declared: declaredValue,
				Actual:   actualValue,
			}
		}
	}

	return differences
}

// valuesEqual compares two values for equality using deep comparison
func (c *Calculator) valuesEqual(a, b interface{}) bool {
	return reflect.DeepEqual(a, b)
}
