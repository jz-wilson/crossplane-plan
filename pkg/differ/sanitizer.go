package differ

import (
	"reflect"
	"regexp"
	"strings"

	"github.com/millstonehq/crossplane-plan/pkg/config"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Sanitizer strips noise fields from XRs before diff
type Sanitizer struct {
	rules []config.StripRule
}

// NewSanitizer creates a new Sanitizer with the given strip rules
func NewSanitizer(rules []config.StripRule) *Sanitizer {
	return &Sanitizer{
		rules: rules,
	}
}

// SanitizeResult contains the sanitized XR and what was stripped
type SanitizeResult struct {
	// SanitizedXR is the XR with noise fields removed
	SanitizedXR *unstructured.Unstructured

	// StrippedFields tracks what was removed for transparency
	StrippedFields []StrippedField
}

// Sanitize applies strip rules to an XR and returns a sanitized copy
func (s *Sanitizer) Sanitize(xr *unstructured.Unstructured) *SanitizeResult {
	// Deep copy to avoid modifying the original
	sanitized := xr.DeepCopy()

	result := &SanitizeResult{
		SanitizedXR:    sanitized,
		StrippedFields: []StrippedField{},
	}

	// Apply each strip rule
	for _, rule := range s.rules {
		s.applyRule(sanitized, rule, result)
	}

	return result
}

// applyRule applies a single strip rule to the XR
func (s *Sanitizer) applyRule(xr *unstructured.Unstructured, rule config.StripRule, result *SanitizeResult) {
	// Parse the path (e.g., "spec.managementPolicies" -> ["spec", "managementPolicies"])
	pathParts := strings.Split(rule.Path, ".")

	// Special handling for annotations and labels (pattern matching)
	if rule.Pattern != "" {
		if rule.Path == "metadata.annotations" {
			s.stripMatchingAnnotations(xr, rule, result)
			return
		}
		if rule.Path == "metadata.labels" {
			s.stripMatchingLabels(xr, rule, result)
			return
		}
	}

	// Get the field value
	value, found, err := unstructured.NestedFieldNoCopy(xr.Object, pathParts...)
	if err != nil || !found {
		return // Field doesn't exist, nothing to strip
	}

	// Check if we should strip based on rule
	if !s.shouldStrip(value, rule) {
		return // Value doesn't match strip condition
	}

	// Strip the field
	unstructured.RemoveNestedField(xr.Object, pathParts...)

	// Track what was stripped
	result.StrippedFields = append(result.StrippedFields, StrippedField{
		Path:   rule.Path,
		Reason: rule.Reason,
	})
}

// shouldStrip checks if a value matches the strip rule conditions
func (s *Sanitizer) shouldStrip(value interface{}, rule config.StripRule) bool {
	// If Equals is specified, check for exact match
	if rule.Equals != nil {
		return s.valuesEqual(value, rule.Equals)
	}

	// Pattern matching is handled separately for annotations/labels
	return false
}

// valuesEqual compares two values for equality
func (s *Sanitizer) valuesEqual(a, b interface{}) bool {
	// Special handling for slices that might be []interface{} vs []string
	aVal := reflect.ValueOf(a)
	bVal := reflect.ValueOf(b)

	// Both must be slices
	if aVal.Kind() == reflect.Slice && bVal.Kind() == reflect.Slice {
		if aVal.Len() != bVal.Len() {
			return false
		}

		// Compare each element
		for i := 0; i < aVal.Len(); i++ {
			aElem := aVal.Index(i).Interface()
			bElem := bVal.Index(i).Interface()

			// Convert both to string for comparison
			aStr, aOk := aElem.(string)
			bStr, bOk := bElem.(string)

			if aOk && bOk {
				if aStr != bStr {
					return false
				}
			} else if !reflect.DeepEqual(aElem, bElem) {
				return false
			}
		}
		return true
	}

	// Fall back to deep equal for non-slice types
	return reflect.DeepEqual(a, b)
}

// stripMatchingAnnotations strips annotations matching a pattern
func (s *Sanitizer) stripMatchingAnnotations(xr *unstructured.Unstructured, rule config.StripRule, result *SanitizeResult) {
	annotations := xr.GetAnnotations()
	if annotations == nil {
		return
	}

	// Compile regex pattern
	pattern, err := regexp.Compile(rule.Pattern)
	if err != nil {
		return // Invalid pattern, skip
	}

	// Find matching annotations
	var strippedKeys []string
	for key := range annotations {
		if pattern.MatchString(key) {
			strippedKeys = append(strippedKeys, key)
		}
	}

	// Strip matching annotations
	if len(strippedKeys) > 0 {
		for _, key := range strippedKeys {
			delete(annotations, key)
		}
		xr.SetAnnotations(annotations)

		// Track what was stripped (one entry per pattern, not per key)
		result.StrippedFields = append(result.StrippedFields, StrippedField{
			Path:   rule.Path + " (pattern: " + rule.Pattern + ")",
			Reason: rule.Reason,
		})
	}
}

// stripMatchingLabels strips labels matching a pattern
func (s *Sanitizer) stripMatchingLabels(xr *unstructured.Unstructured, rule config.StripRule, result *SanitizeResult) {
	labels := xr.GetLabels()
	if labels == nil {
		return
	}

	// Compile regex pattern
	pattern, err := regexp.Compile(rule.Pattern)
	if err != nil {
		return // Invalid pattern, skip
	}

	// Find matching labels
	var strippedKeys []string
	for key := range labels {
		if pattern.MatchString(key) {
			strippedKeys = append(strippedKeys, key)
		}
	}

	// Strip matching labels
	if len(strippedKeys) > 0 {
		for _, key := range strippedKeys {
			delete(labels, key)
		}
		xr.SetLabels(labels)

		// Track what was stripped (one entry per pattern, not per key)
		result.StrippedFields = append(result.StrippedFields, StrippedField{
			Path:   rule.Path + " (pattern: " + rule.Pattern + ")",
			Reason: rule.Reason,
		})
	}
}
