package config

import (
	"bytes"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// DefaultStripRules returns the built-in default strip rules
func DefaultStripRules() []StripRule {
	return []StripRule{
		{
			Path:   "spec.managementPolicies",
			Equals: []interface{}{"Observe"},
			Reason: "PR previews forced to read-only mode for safety",
		},
		{
			Path:    "metadata.annotations",
			Pattern: `^argocd\.argoproj\.io/.*`,
			Reason:  "ArgoCD-managed tracking metadata",
		},
		{
			Path:    "metadata.labels",
			Pattern: `^crossplane\.io/composite$`,
			Reason:  "Crossplane composite resource tracking label",
		},
	}
}

// LoadConfig loads configuration from a file
func LoadConfig(path string) (*Config, error) {
	// Default config
	cfg := DefaultConfig()

	// If no path specified, return defaults
	if path == "" {
		return cfg, nil
	}

	// Read config file
	data, err := os.ReadFile(path)
	if err != nil {
		// If file doesn't exist, return defaults
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	// Parse YAML with schema checking so legacy top-level GitHub settings fail
	// instead of being silently ignored during provider migration.
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	return cfg, nil
}

// GetAllStripRules returns all active strip rules (defaults + user-defined)
func (c *Config) GetAllStripRules() []StripRule {
	var rules []StripRule

	// Add default rules if enabled
	if c.Diff.StripDefaults {
		rules = append(rules, DefaultStripRules()...)
	}

	// Add user-defined rules
	rules = append(rules, c.Diff.StripRules...)

	return rules
}
