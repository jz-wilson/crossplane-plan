package config

// StripRule defines a rule for stripping fields from XRs before diff
type StripRule struct {
	// Path is the JSONPath to the field (e.g., "spec.managementPolicies")
	Path string `yaml:"path"`

	// Equals specifies an exact value match for stripping
	// Only strip if the field equals this value
	Equals interface{} `yaml:"equals,omitempty"`

	// Pattern is a regex pattern for matching (used for annotations, labels)
	Pattern string `yaml:"pattern,omitempty"`

	// Reason explains why this field is being stripped (shown in PR comment footer)
	Reason string `yaml:"reason"`
}

// DiffConfig controls diff behavior
type DiffConfig struct {
	// StripDefaults enables the built-in default strip rules
	StripDefaults bool `yaml:"stripDefaults"`

	// StripRules are additional user-defined strip rules
	StripRules []StripRule `yaml:"stripRules,omitempty"`
}

// Config holds the application configuration
type Config struct {
	// DetectionStrategy defines how to extract PR numbers from XRs
	// Supported values: "name", "label", "annotation"
	DetectionStrategy string `yaml:"-"` // From CLI flag, not config file

	// NamePattern is the pattern used for name-based detection
	// Example: "pr-{number}-*" matches "pr-123-mill"
	NamePattern string `yaml:"-"` // From CLI flag, not config file

	// GitHubRepo is the target repository for posting comments
	// Format: "owner/repo"
	GitHubRepo string `yaml:"-"` // From CLI flag, not config file

	// DryRun mode calculates diffs but doesn't post to GitHub
	DryRun bool `yaml:"-"` // From CLI flag, not config file

	// OutputFormat controls how diffs are rendered.
	// Supported values: "github" (GitHub-flavored markdown, default),
	// "json" (structured JSON for programmatic consumers)
	OutputFormat string `yaml:"-"` // From CLI flag, not config file

	// LabelKey is the label key for label-based detection
	// Default: "millstone.tech/pr-number"
	LabelKey string `yaml:"-"` // From CLI flag, not config file

	// AnnotationKey is the annotation key for annotation-based detection
	// Default: "millstone.tech/preview-pr"
	AnnotationKey string `yaml:"-"` // From CLI flag, not config file

	// Diff controls diff calculation and formatting
	Diff DiffConfig `yaml:"diff"`
}

// DefaultConfig returns a Config with sensible defaults
func DefaultConfig() *Config {
	return &Config{
		DetectionStrategy: "name",
		NamePattern:       "pr-{number}-*",
		LabelKey:          "millstone.tech/pr-number",
		AnnotationKey:     "millstone.tech/preview-pr",
		OutputFormat:      "github",
		Diff: DiffConfig{
			StripDefaults: true,
			StripRules:    []StripRule{},
		},
	}
}
