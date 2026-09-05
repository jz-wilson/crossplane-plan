package config

// StripRule defines a rule for stripping fields from XRs before diff.
type StripRule struct {
	// Path is the JSONPath to the field (e.g., "spec.managementPolicies").
	Path string `yaml:"path"`

	// Equals specifies an exact value match for stripping.
	Equals any `yaml:"equals,omitempty"`

	// Pattern is a regex pattern for matching annotations or labels.
	Pattern string `yaml:"pattern,omitempty"`

	// Reason explains why this field is being stripped.
	Reason string `yaml:"reason"`
}

// DiffConfig controls diff behavior.
type DiffConfig struct {
	// StripDefaults enables the built-in default strip rules.
	StripDefaults bool `yaml:"stripDefaults"`

	// StripRules are additional user-defined strip rules.
	StripRules []StripRule `yaml:"stripRules,omitempty"`
}

// SecretKeyRef identifies one key in a Kubernetes Secret.
type SecretKeyRef struct {
	Name string `yaml:"name"`
	Key  string `yaml:"key"`
}

// GitHubConfig contains non-secret GitHub provider configuration.
type GitHubConfig struct {
	Repository string `yaml:"repository"`
}

// AzureReposAuthConfig contains Azure Repos authentication configuration.
type AzureReposAuthConfig struct {
	Mode           string        `yaml:"mode"`
	TokenSecretRef *SecretKeyRef `yaml:"tokenSecretRef,omitempty"`
}

// AzureReposConfig contains Azure Repos provider configuration.
type AzureReposConfig struct {
	Organization string               `yaml:"organization"`
	ProjectID    string               `yaml:"projectId"`
	RepositoryID string               `yaml:"repositoryId"`
	Auth         AzureReposAuthConfig `yaml:"auth"`
}

// VCSConfig selects one provider and contains its provider-specific settings.
type VCSConfig struct {
	Provider   string           `yaml:"provider"`
	GitHub     GitHubConfig     `yaml:"github"`
	AzureRepos AzureReposConfig `yaml:"azureRepos"`
}

// Config holds application configuration.
type Config struct {
	// DetectionStrategy defines how to extract PR numbers from XRs.
	DetectionStrategy string `yaml:"-"`

	// NamePattern is the pattern used for name-based detection.
	NamePattern string `yaml:"-"`

	// DryRun calculates diffs without posting comments.
	DryRun bool `yaml:"-"`

	// OutputFormat controls how diffs are rendered.
	OutputFormat string `yaml:"-"`

	// LabelKey is the label key for label-based detection.
	LabelKey string `yaml:"-"`

	// AnnotationKey is the annotation key for annotation-based detection.
	AnnotationKey string `yaml:"-"`

	// VCS contains provider selection and provider-specific configuration.
	VCS VCSConfig `yaml:"vcs"`

	// Diff controls diff calculation and formatting.
	Diff DiffConfig `yaml:"diff"`
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		DetectionStrategy: "name",
		NamePattern:       "pr-{number}-*",
		LabelKey:          "millstone.tech/pr-number",
		AnnotationKey:     "millstone.tech/preview-pr",
		OutputFormat:      "github",
		VCS: VCSConfig{
			Provider: "github",
		},
		Diff: DiffConfig{
			StripDefaults: true,
			StripRules:    []StripRule{},
		},
	}
}
