package argocd

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

var (
	// ErrNotFound indicates ArgoCD Application or ArgoCD itself is not available
	ErrNotFound = fmt.Errorf("argocd application not found")
)

// Client handles interactions with ArgoCD Applications
type Client struct {
	dynamicClient dynamic.Interface
	namespace     string // ArgoCD namespace
	logger        logr.Logger
	prPrefix      string // e.g., "pr-"
	prSuffix      string // e.g., "" (not commonly used)
}

// AppDiff represents the difference between two ArgoCD Applications
type AppDiff struct {
	Additions     []ResourceChange
	Modifications []ResourceChange
	Deletions     []ResourceDeletion
	RawDiff       string
}

// ResourceChange represents a resource being added or modified
type ResourceChange struct {
	GVK       schema.GroupVersionKind
	Name      string
	Namespace string
	RawDiff   string
}

// ResourceDeletion represents a resource being deleted
type ResourceDeletion struct {
	GVK       schema.GroupVersionKind
	Name      string
	Namespace string
	RawDiff   string
}

// NewClient creates a new ArgoCD client
func NewClient(dynamicClient dynamic.Interface, namespace, prPrefix, prSuffix string, logger logr.Logger) *Client {
	return &Client{
		dynamicClient: dynamicClient,
		namespace:     namespace,
		logger:        logger,
		prPrefix:      prPrefix,
		prSuffix:      prSuffix,
	}
}

// GetProductionAppName strips PR prefix/suffix to get production app name
// Example: "pr-123-myapp" with prefix "pr-" → "myapp"
func (c *Client) GetProductionAppName(prAppName string) string {
	result := prAppName

	// Strip prefix (e.g., "pr-123-")
	if c.prPrefix != "" {
		// Match pattern like "pr-{number}-"
		pattern := regexp.MustCompile(fmt.Sprintf(`^%s\d+[-_]`, regexp.QuoteMeta(c.prPrefix)))
		result = pattern.ReplaceAllString(result, "")
	}

	// Strip suffix (e.g., "-pr-123")
	if c.prSuffix != "" {
		pattern := regexp.MustCompile(fmt.Sprintf(`%s[-_]\d+$`, regexp.QuoteMeta(c.prSuffix)))
		result = pattern.ReplaceAllString(result, "")
	}

	return result
}

// GetAppDiff compares two ArgoCD Applications and returns the diff
func (c *Client) GetAppDiff(ctx context.Context, prAppName, prodAppName string) (*AppDiff, error) {
	// Get both applications
	prApp, err := c.getApplication(ctx, prAppName)
	if err != nil {
		return nil, fmt.Errorf("failed to get PR application %s: %w", prAppName, err)
	}

	prodApp, err := c.getApplication(ctx, prodAppName)
	if err != nil {
		// Production app might not exist (new app scenario)
		c.logger.Info("Production application not found, treating as new deployment", "app", prodAppName)
		prResources := c.extractResourcesFromApp(prApp, "pr")

		// All PR resources are additions
		additions := make([]ResourceChange, 0, len(prResources))
		for _, res := range prResources {
			additions = append(additions, ResourceChange{
				GVK:       res.GVK(),
				Name:      res.Name,
				Namespace: res.Namespace,
			})
		}

		return &AppDiff{
			Additions: additions,
		}, nil
	}

	// Extract resources from both apps
	prResources := c.extractResourcesFromApp(prApp, "pr")
	prodResources := c.extractResourcesFromApp(prodApp, "prod")

	// Compare and build diff
	diff := c.compareResources(prResources, prodResources)

	return diff, nil
}

// getApplication retrieves an ArgoCD Application by name
func (c *Client) getApplication(ctx context.Context, name string) (*unstructured.Unstructured, error) {
	gvr := schema.GroupVersionResource{
		Group:    "argoproj.io",
		Version:  "v1alpha1",
		Resource: "applications",
	}

	app, err := c.dynamicClient.Resource(gvr).Namespace(c.namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, err)
	}

	return app, nil
}

// extractResourcesFromApp extracts managed resources from an ArgoCD Application
func (c *Client) extractResourcesFromApp(app *unstructured.Unstructured, context string) map[string]*ResourceInfo {
	resources := make(map[string]*ResourceInfo)

	// Extract from status.resources
	statusResources, found, err := unstructured.NestedSlice(app.Object, "status", "resources")
	if !found || err != nil {
		c.logger.Info("No resources found in application status", "app", app.GetName(), "context", context)
		return resources
	}

	for _, res := range statusResources {
		resMap, ok := res.(map[string]interface{})
		if !ok {
			continue
		}

		ri := &ResourceInfo{
			Group:     getStringField(resMap, "group"),
			Version:   getStringField(resMap, "version"),
			Kind:      getStringField(resMap, "kind"),
			Name:      getStringField(resMap, "name"),
			Namespace: getStringField(resMap, "namespace"),
		}

		// Create unique key
		key := ri.Key()
		resources[key] = ri
	}

	return resources
}

// ResourceInfo holds information about a managed resource
type ResourceInfo struct {
	Group     string
	Version   string
	Kind      string
	Name      string
	Namespace string
}

// Key creates a unique key for the resource
func (ri *ResourceInfo) Key() string {
	return fmt.Sprintf("%s/%s/%s/%s/%s", ri.Group, ri.Version, ri.Kind, ri.Namespace, ri.Name)
}

// GVK returns the GroupVersionKind
func (ri *ResourceInfo) GVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{
		Group:   ri.Group,
		Version: ri.Version,
		Kind:    ri.Kind,
	}
}

// compareResources compares PR and production resources to find additions, modifications, and deletions
func (c *Client) compareResources(prResources, prodResources map[string]*ResourceInfo) *AppDiff {
	diff := &AppDiff{
		Additions:     []ResourceChange{},
		Modifications: []ResourceChange{},
		Deletions:     []ResourceDeletion{},
	}

	// Find additions and modifications
	for key, prRes := range prResources {
		if _, exists := prodResources[key]; !exists {
			// New resource
			diff.Additions = append(diff.Additions, ResourceChange{
				GVK:       prRes.GVK(),
				Name:      prRes.Name,
				Namespace: prRes.Namespace,
			})
		} else {
			// Resource exists in both - could be modified
			// Note: We can't detect actual content changes without diffing manifests
			// ArgoCD would show this, but for now we just track that it exists
			diff.Modifications = append(diff.Modifications, ResourceChange{
				GVK:       prRes.GVK(),
				Name:      prRes.Name,
				Namespace: prRes.Namespace,
			})
		}
	}

	// Find deletions
	for key, prodRes := range prodResources {
		if _, exists := prResources[key]; !exists {
			// Resource in production but not in PR - will be deleted
			diff.Deletions = append(diff.Deletions, ResourceDeletion{
				GVK:       prodRes.GVK(),
				Name:      prodRes.Name,
				Namespace: prodRes.Namespace,
				RawDiff:   fmt.Sprintf("- %s/%s (%s)", prodRes.Kind, prodRes.Name, prodRes.Namespace),
			})
		}
	}

	return diff
}

// getStringField safely extracts a string field from a map
func getStringField(m map[string]interface{}, field string) string {
	if val, ok := m[field].(string); ok {
		return val
	}
	return ""
}

// ParseDiffOutput parses argocd app diff output (for future use with exec-based approach)
func (c *Client) ParseDiffOutput(diffText string) (*AppDiff, error) {
	diff := &AppDiff{
		RawDiff:       diffText,
		Additions:     []ResourceChange{},
		Modifications: []ResourceChange{},
		Deletions:     []ResourceDeletion{},
	}

	// Parse unified diff format
	// This is a simplified parser - production version would need more robust parsing
	lines := strings.Split(diffText, "\n")

	var currentResource *ResourceInfo
	var currentDiff strings.Builder

	for _, line := range lines {
		// Look for resource headers (e.g., "apiVersion: v1" followed by "kind: Pod")
		if strings.HasPrefix(line, "===") || strings.HasPrefix(line, "---") || strings.HasPrefix(line, "+++") {
			// Save previous resource if exists
			if currentResource != nil {
				c.addParsedResource(diff, currentResource, currentDiff.String())
				currentResource = nil
				currentDiff.Reset()
			}
			continue
		}

		currentDiff.WriteString(line)
		currentDiff.WriteString("\n")
	}

	// Save last resource
	if currentResource != nil {
		c.addParsedResource(diff, currentResource, currentDiff.String())
	}

	return diff, nil
}

// addParsedResource adds a parsed resource to the appropriate diff category
func (c *Client) addParsedResource(diff *AppDiff, res *ResourceInfo, rawDiff string) {
	// Determine if it's an addition, modification, or deletion based on diff markers
	if strings.Contains(rawDiff, "---") && !strings.Contains(rawDiff, "+++") {
		diff.Deletions = append(diff.Deletions, ResourceDeletion{
			GVK:       res.GVK(),
			Name:      res.Name,
			Namespace: res.Namespace,
			RawDiff:   rawDiff,
		})
	} else if strings.Contains(rawDiff, "+++") && !strings.Contains(rawDiff, "---") {
		diff.Additions = append(diff.Additions, ResourceChange{
			GVK:       res.GVK(),
			Name:      res.Name,
			Namespace: res.Namespace,
			RawDiff:   rawDiff,
		})
	} else {
		diff.Modifications = append(diff.Modifications, ResourceChange{
			GVK:       res.GVK(),
			Name:      res.Name,
			Namespace: res.Namespace,
			RawDiff:   rawDiff,
		})
	}
}
