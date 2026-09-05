package argocd

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
)

func TestGetProductionAppName(t *testing.T) {
	tests := []struct {
		name       string
		prAppName  string
		prPrefix   string
		prSuffix   string
		wantResult string
	}{
		{
			name:       "standard prefix pattern",
			prAppName:  "pr-123-myapp",
			prPrefix:   "pr-",
			prSuffix:   "",
			wantResult: "myapp",
		},
		{
			name:       "no prefix",
			prAppName:  "myapp",
			prPrefix:   "",
			prSuffix:   "",
			wantResult: "myapp",
		},
		{
			name:       "suffix pattern",
			prAppName:  "myapp-pr-123",
			prPrefix:   "",
			prSuffix:   "-pr",
			wantResult: "myapp",
		},
		{
			name:       "both prefix and suffix",
			prAppName:  "pr-123-myapp-test",
			prPrefix:   "pr-",
			prSuffix:   "",
			wantResult: "myapp-test",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &Client{
				prPrefix: tt.prPrefix,
				prSuffix: tt.prSuffix,
			}

			result := client.GetProductionAppName(tt.prAppName)
			if result != tt.wantResult {
				t.Errorf("GetProductionAppName() = %v, want %v", result, tt.wantResult)
			}
		})
	}
}

func TestGetAppDiff(t *testing.T) {
	scheme := runtime.NewScheme()

	prApp := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "argoproj.io/v1alpha1",
			"kind":       "Application",
			"metadata": map[string]interface{}{
				"name":      "pr-123-myapp",
				"namespace": "argocd",
			},
			"status": map[string]interface{}{
				"resources": []interface{}{
					map[string]interface{}{
						"group":     "apps",
						"version":   "v1",
						"kind":      "Deployment",
						"name":      "pr-123-deployment",
						"namespace": "default",
					},
					map[string]interface{}{
						"group":     "",
						"version":   "v1",
						"kind":      "Service",
						"name":      "pr-123-service",
						"namespace": "default",
					},
				},
			},
		},
	}
	prApp.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "argoproj.io",
		Version: "v1alpha1",
		Kind:    "Application",
	})

	prodApp := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "argoproj.io/v1alpha1",
			"kind":       "Application",
			"metadata": map[string]interface{}{
				"name":      "myapp",
				"namespace": "argocd",
			},
			"status": map[string]interface{}{
				"resources": []interface{}{
					map[string]interface{}{
						"group":     "apps",
						"version":   "v1",
						"kind":      "Deployment",
						"name":      "prod-deployment",
						"namespace": "default",
					},
				},
			},
		},
	}
	prodApp.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "argoproj.io",
		Version: "v1alpha1",
		Kind:    "Application",
	})

	dynamicClient := fake.NewSimpleDynamicClient(scheme, prApp, prodApp)

	client := &Client{
		dynamicClient: dynamicClient,
		namespace:     "argocd",
		prPrefix:      "pr-",
		prSuffix:      "",
		logger:        logr.Discard(),
	}

	ctx := context.Background()
	diff, err := client.GetAppDiff(ctx, "pr-123-myapp", "myapp")
	if err != nil {
		t.Fatalf("GetAppDiff() error = %v", err)
	}

	// PR has 2 resources, production has 1
	// Expected: 1 addition (pr-123-service), 1 modification (deployments match in count but different names)
	if len(diff.Additions) == 0 {
		t.Error("Expected additions, got none")
	}

	if len(diff.Deletions) == 0 {
		t.Error("Expected deletions (prod-deployment not in PR), got none")
	}
}

func TestGetAppDiff_ProductionNotFound(t *testing.T) {
	scheme := runtime.NewScheme()

	prApp := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "argoproj.io/v1alpha1",
			"kind":       "Application",
			"metadata": map[string]interface{}{
				"name":      "pr-123-myapp",
				"namespace": "argocd",
			},
			"status": map[string]interface{}{
				"resources": []interface{}{
					map[string]interface{}{
						"group":     "apps",
						"version":   "v1",
						"kind":      "Deployment",
						"name":      "pr-123-deployment",
						"namespace": "default",
					},
				},
			},
		},
	}
	prApp.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "argoproj.io",
		Version: "v1alpha1",
		Kind:    "Application",
	})

	dynamicClient := fake.NewSimpleDynamicClient(scheme, prApp)

	client := &Client{
		dynamicClient: dynamicClient,
		namespace:     "argocd",
		prPrefix:      "pr-",
		prSuffix:      "",
		logger:        logr.Discard(),
	}

	ctx := context.Background()
	diff, err := client.GetAppDiff(ctx, "pr-123-myapp", "myapp")
	if err != nil {
		t.Fatalf("GetAppDiff() error = %v", err)
	}

	// Production app doesn't exist, so all PR resources are additions
	if len(diff.Additions) != 1 {
		t.Errorf("Expected 1 addition, got %d", len(diff.Additions))
	}

	if len(diff.Deletions) != 0 {
		t.Errorf("Expected no deletions, got %d", len(diff.Deletions))
	}
}

func TestCompareResources(t *testing.T) {
	client := &Client{
		logger: logr.Discard(),
	}

	prResources := map[string]*ResourceInfo{
		"apps/v1/Deployment/default/pr-deployment": {
			Group:     "apps",
			Version:   "v1",
			Kind:      "Deployment",
			Name:      "pr-deployment",
			Namespace: "default",
		},
		"v1/Service/default/pr-service": {
			Group:     "",
			Version:   "v1",
			Kind:      "Service",
			Name:      "pr-service",
			Namespace: "default",
		},
	}

	prodResources := map[string]*ResourceInfo{
		"apps/v1/Deployment/default/prod-deployment": {
			Group:     "apps",
			Version:   "v1",
			Kind:      "Deployment",
			Name:      "prod-deployment",
			Namespace: "default",
		},
		"v1/Service/default/pr-service": {
			Group:     "",
			Version:   "v1",
			Kind:      "Service",
			Name:      "pr-service",
			Namespace: "default",
		},
	}

	diff := client.compareResources(prResources, prodResources)

	// pr-deployment is new (addition)
	if len(diff.Additions) != 1 {
		t.Errorf("Expected 1 addition, got %d", len(diff.Additions))
	}

	// pr-service exists in both (modification)
	if len(diff.Modifications) != 1 {
		t.Errorf("Expected 1 modification, got %d", len(diff.Modifications))
	}

	// prod-deployment is deleted
	if len(diff.Deletions) != 1 {
		t.Errorf("Expected 1 deletion, got %d", len(diff.Deletions))
	}

	// Check deletion details
	if len(diff.Deletions) > 0 {
		deletion := diff.Deletions[0]
		if deletion.Name != "prod-deployment" {
			t.Errorf("Expected deletion name 'prod-deployment', got '%s'", deletion.Name)
		}
		if deletion.GVK.Kind != "Deployment" {
			t.Errorf("Expected deletion kind 'Deployment', got '%s'", deletion.GVK.Kind)
		}
	}
}

func TestResourceInfo_Key(t *testing.T) {
	ri := &ResourceInfo{
		Group:     "apps",
		Version:   "v1",
		Kind:      "Deployment",
		Name:      "my-deployment",
		Namespace: "default",
	}

	expected := "apps/v1/Deployment/default/my-deployment"
	result := ri.Key()

	if result != expected {
		t.Errorf("Key() = %v, want %v", result, expected)
	}
}

func TestResourceInfo_GVK(t *testing.T) {
	ri := &ResourceInfo{
		Group:     "apps",
		Version:   "v1",
		Kind:      "Deployment",
		Name:      "my-deployment",
		Namespace: "default",
	}

	gvk := ri.GVK()

	if gvk.Group != "apps" {
		t.Errorf("GVK().Group = %v, want 'apps'", gvk.Group)
	}
	if gvk.Version != "v1" {
		t.Errorf("GVK().Version = %v, want 'v1'", gvk.Version)
	}
	if gvk.Kind != "Deployment" {
		t.Errorf("GVK().Kind = %v, want 'Deployment'", gvk.Kind)
	}
}

func TestNewClient(t *testing.T) {
	scheme := runtime.NewScheme()
	dynamicClient := fake.NewSimpleDynamicClient(scheme)
	logger := logr.Discard()

	client := NewClient(dynamicClient, "argocd", "pr-", "", logger)

	if client == nil {
		t.Fatal("NewClient() returned nil")
	}
	if client.namespace != "argocd" {
		t.Errorf("namespace = %v, want 'argocd'", client.namespace)
	}
	if client.prPrefix != "pr-" {
		t.Errorf("prPrefix = %v, want 'pr-'", client.prPrefix)
	}
	if client.prSuffix != "" {
		t.Errorf("prSuffix = %v, want ''", client.prSuffix)
	}
}

func TestExtractResourcesFromApp(t *testing.T) {
	client := &Client{
		logger: logr.Discard(),
	}

	tests := []struct {
		name    string
		app     *unstructured.Unstructured
		wantLen int
	}{
		{
			name: "app with resources",
			app: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"status": map[string]interface{}{
						"resources": []interface{}{
							map[string]interface{}{
								"group":     "apps",
								"version":   "v1",
								"kind":      "Deployment",
								"name":      "test-deploy",
								"namespace": "default",
							},
							map[string]interface{}{
								"group":     "",
								"version":   "v1",
								"kind":      "Service",
								"name":      "test-svc",
								"namespace": "default",
							},
						},
					},
				},
			},
			wantLen: 2,
		},
		{
			name: "app without status",
			app: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"name": "test-app",
					},
				},
			},
			wantLen: 0,
		},
		{
			name: "app with empty resources",
			app: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"status": map[string]interface{}{
						"resources": []interface{}{},
					},
				},
			},
			wantLen: 0,
		},
		{
			name: "app with invalid resource entry",
			app: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"status": map[string]interface{}{
						"resources": []interface{}{
							"invalid-string-entry",
							map[string]interface{}{
								"group":     "apps",
								"version":   "v1",
								"kind":      "Deployment",
								"name":      "test-deploy",
								"namespace": "default",
							},
						},
					},
				},
			},
			wantLen: 1, // Should skip invalid entry
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resources := client.extractResourcesFromApp(tt.app, "test")
			if len(resources) != tt.wantLen {
				t.Errorf("extractResourcesFromApp() returned %d resources, want %d", len(resources), tt.wantLen)
			}
		})
	}
}

func TestGetAppDiff_ErrorGettingPRApp(t *testing.T) {
	scheme := runtime.NewScheme()
	// Empty client - both apps will fail to fetch
	dynamicClient := fake.NewSimpleDynamicClient(scheme)

	client := &Client{
		dynamicClient: dynamicClient,
		namespace:     "argocd",
		prPrefix:      "pr-",
		prSuffix:      "",
		logger:        logr.Discard(),
	}

	ctx := context.Background()
	_, err := client.GetAppDiff(ctx, "pr-123-nonexistent", "nonexistent")
	if err == nil {
		t.Error("Expected error when PR app doesn't exist, got nil")
	}
}

func TestParseDiffOutput(t *testing.T) {
	client := &Client{
		logger: logr.Discard(),
	}

	tests := []struct {
		name     string
		diffText string
		wantErr  bool
	}{
		{
			name:     "empty diff",
			diffText: "",
			wantErr:  false,
		},
		{
			name: "simple diff",
			diffText: `===
apiVersion: v1
kind: Service
---
name: test
+++
name: test-new`,
			wantErr: false,
		},
		{
			name: "multi-line diff",
			diffText: `===
--- Deployment/test
+++ Deployment/test-new
some changes here
===
another resource`,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diff, err := client.ParseDiffOutput(tt.diffText)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseDiffOutput() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if diff == nil {
				t.Error("ParseDiffOutput() returned nil diff")
			}
			if diff.RawDiff != tt.diffText {
				t.Error("ParseDiffOutput() didn't preserve RawDiff")
			}
		})
	}
}

func TestGetStringField(t *testing.T) {
	tests := []struct {
		name      string
		m         map[string]interface{}
		field     string
		wantValue string
	}{
		{
			name:      "valid string field",
			m:         map[string]interface{}{"key": "value"},
			field:     "key",
			wantValue: "value",
		},
		{
			name:      "missing field",
			m:         map[string]interface{}{"other": "value"},
			field:     "key",
			wantValue: "",
		},
		{
			name:      "non-string field",
			m:         map[string]interface{}{"key": 123},
			field:     "key",
			wantValue: "",
		},
		{
			name:      "nil field",
			m:         map[string]interface{}{"key": nil},
			field:     "key",
			wantValue: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getStringField(tt.m, tt.field)
			if result != tt.wantValue {
				t.Errorf("getStringField() = %v, want %v", result, tt.wantValue)
			}
		})
	}
}
