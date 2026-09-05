package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg == nil {
		t.Fatal("DefaultConfig() returned nil")
	}

	// Check defaults
	if cfg.DetectionStrategy != "name" {
		t.Errorf("DetectionStrategy = %s, want name", cfg.DetectionStrategy)
	}

	if cfg.NamePattern != "pr-{number}-*" {
		t.Errorf("NamePattern = %s, want pr-{number}-*", cfg.NamePattern)
	}

	if cfg.LabelKey != "millstone.tech/pr-number" {
		t.Errorf("LabelKey = %s, want millstone.tech/pr-number", cfg.LabelKey)
	}

	if cfg.AnnotationKey != "millstone.tech/preview-pr" {
		t.Errorf("AnnotationKey = %s, want millstone.tech/preview-pr", cfg.AnnotationKey)
	}

	if cfg.OutputFormat != "github" {
		t.Errorf("OutputFormat = %s, want github", cfg.OutputFormat)
	}

	// Check Diff defaults
	if !cfg.Diff.StripDefaults {
		t.Error("Diff.StripDefaults should be true by default")
	}

	if len(cfg.Diff.StripRules) != 0 {
		t.Errorf("Diff.StripRules length = %d, want 0", len(cfg.Diff.StripRules))
	}
}

func TestLoadConfig_EmptyPath(t *testing.T) {
	cfg, err := LoadConfig("")
	if err != nil {
		t.Fatalf("LoadConfig() with empty path error = %v, want nil", err)
	}

	if cfg == nil {
		t.Fatal("LoadConfig() with empty path returned nil config")
	}

	// Should return default config
	if !cfg.Diff.StripDefaults {
		t.Error("Expected default config with StripDefaults = true")
	}
}

func TestLoadConfig_NonExistentFile(t *testing.T) {
	cfg, err := LoadConfig("/nonexistent/config.yaml")
	if err != nil {
		t.Fatalf("LoadConfig() error = %v, want nil (should return defaults)", err)
	}

	if cfg == nil {
		t.Fatal("LoadConfig() returned nil config")
	}

	// Should return default config when file doesn't exist
	if !cfg.Diff.StripDefaults {
		t.Error("Expected default config when file doesn't exist")
	}
}

func TestLoadConfig_ValidFile(t *testing.T) {
	// Create temporary config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	configYAML := `diff:
  stripDefaults: false
  stripRules:
    - path: "metadata.labels"
      pattern: "^custom\\.io/.*"
      reason: "Custom labels"
    - path: "spec.someField"
      equals: "testValue"
      reason: "Test field"
`

	if err := os.WriteFile(configPath, []byte(configYAML), 0644); err != nil {
		t.Fatalf("Failed to write test config: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v, want nil", err)
	}

	if cfg == nil {
		t.Fatal("LoadConfig() returned nil config")
	}

	// Check parsed values
	if cfg.Diff.StripDefaults {
		t.Error("Diff.StripDefaults = true, want false")
	}

	if len(cfg.Diff.StripRules) != 2 {
		t.Fatalf("len(StripRules) = %d, want 2", len(cfg.Diff.StripRules))
	}

	// Check first rule
	if cfg.Diff.StripRules[0].Path != "metadata.labels" {
		t.Errorf("StripRules[0].Path = %s, want metadata.labels", cfg.Diff.StripRules[0].Path)
	}
	if cfg.Diff.StripRules[0].Pattern != "^custom\\.io/.*" {
		t.Errorf("StripRules[0].Pattern = %s, want ^custom\\.io/.*", cfg.Diff.StripRules[0].Pattern)
	}

	// Check second rule
	if cfg.Diff.StripRules[1].Path != "spec.someField" {
		t.Errorf("StripRules[1].Path = %s, want spec.someField", cfg.Diff.StripRules[1].Path)
	}
	if cfg.Diff.StripRules[1].Equals != "testValue" {
		t.Errorf("StripRules[1].Equals = %v, want testValue", cfg.Diff.StripRules[1].Equals)
	}
}

func TestLoadConfig_InvalidYAML(t *testing.T) {
	// Create temporary invalid YAML file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "invalid.yaml")

	invalidYAML := `diff:
  stripDefaults: [this is not valid YAML
  - broken
`

	if err := os.WriteFile(configPath, []byte(invalidYAML), 0644); err != nil {
		t.Fatalf("Failed to write test config: %v", err)
	}

	_, err := LoadConfig(configPath)
	if err == nil {
		t.Error("LoadConfig() error = nil, want error for invalid YAML")
	}
}

func TestLoadConfig_VCSProviderConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	configYAML := `vcs:
  provider: azure-repos
  github:
    repository: owner/repo
  azureRepos:
    organization: org
    projectId: 11111111-1111-1111-1111-111111111111
    repositoryId: 22222222-2222-2222-2222-222222222222
    auth:
      mode: workloadIdentity
`
	if err := os.WriteFile(configPath, []byte(configYAML), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg.VCS.Provider != "azure-repos" || cfg.VCS.AzureRepos.ProjectID == "" {
		t.Errorf("VCS config = %+v", cfg.VCS)
	}
}

func TestLoadConfig_RejectsLegacyTopLevelGitHub(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("github:\n  repo: owner/repo\n"), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, err := LoadConfig(configPath); err == nil {
		t.Fatal("LoadConfig() error = nil, want legacy config error")
	}
}

func TestGetAllStripRules_WithDefaults(t *testing.T) {
	cfg := &Config{
		Diff: DiffConfig{
			StripDefaults: true,
			StripRules: []StripRule{
				{
					Path:   "custom.path",
					Reason: "Custom rule",
				},
			},
		},
	}

	rules := cfg.GetAllStripRules()

	// Should have defaults + custom rules
	defaultRules := DefaultStripRules()
	expectedCount := len(defaultRules) + 1

	if len(rules) != expectedCount {
		t.Errorf("len(rules) = %d, want %d", len(rules), expectedCount)
	}

	// Check that default rules are included
	found := false
	for _, rule := range rules {
		if rule.Path == "spec.managementPolicies" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Default rule for spec.managementPolicies not found")
	}

	// Check that custom rule is included
	found = false
	for _, rule := range rules {
		if rule.Path == "custom.path" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Custom rule not found")
	}
}

func TestGetAllStripRules_WithoutDefaults(t *testing.T) {
	cfg := &Config{
		Diff: DiffConfig{
			StripDefaults: false,
			StripRules: []StripRule{
				{
					Path:   "custom.path",
					Reason: "Custom rule",
				},
			},
		},
	}

	rules := cfg.GetAllStripRules()

	// Should only have custom rules
	if len(rules) != 1 {
		t.Errorf("len(rules) = %d, want 1", len(rules))
	}

	if rules[0].Path != "custom.path" {
		t.Errorf("rules[0].Path = %s, want custom.path", rules[0].Path)
	}
}

func TestDefaultStripRules(t *testing.T) {
	rules := DefaultStripRules()

	if len(rules) == 0 {
		t.Fatal("DefaultStripRules() returned empty slice")
	}

	// Check that expected rules exist
	expectedPaths := []string{
		"spec.managementPolicies",
		"metadata.annotations",
		"metadata.labels",
	}

	for _, expectedPath := range expectedPaths {
		found := false
		for _, rule := range rules {
			if rule.Path == expectedPath {
				found = true
				if rule.Reason == "" {
					t.Errorf("Rule for %s has empty Reason", expectedPath)
				}
				break
			}
		}
		if !found {
			t.Errorf("Default rule for %s not found", expectedPath)
		}
	}
}
