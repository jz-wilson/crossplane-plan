package watcher

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	// ArgoCD automatically adds this label to all managed resources
	ArgoCDInstanceLabel = "argocd.argoproj.io/instance"
)

// Scope represents the deployment scope for a PR preview
type Scope struct {
	Type        string // "argocd" or potentially "flux" in the future
	PRAppName   string // ArgoCD Application name for PR (e.g., "pr-123-myapp")
	ProdAppName string // ArgoCD Application name for production (e.g., "myapp")
}

// DiscoverScope extracts ArgoCD application information from XR labels
func (w *XRWatcher) DiscoverScope(xr *unstructured.Unstructured) (*Scope, error) {
	labels := xr.GetLabels()
	if labels == nil {
		return nil, fmt.Errorf(
			"XR %s has no labels. crossplane-plan requires ArgoCD to manage your resources. "+
				"See: https://github.com/millstonehq/crossplane-plan#argocd-setup",
			xr.GetName())
	}

	// ArgoCD automatically adds this label
	appName, ok := labels[ArgoCDInstanceLabel]
	if !ok {
		return nil, fmt.Errorf(
			"XR %s is not managed by ArgoCD (missing %s label). "+
				"crossplane-plan requires ArgoCD. "+
				"See: https://github.com/millstonehq/crossplane-plan#argocd-setup",
			xr.GetName(),
			ArgoCDInstanceLabel)
	}

	// Get production app name by stripping PR prefix
	prodAppName := w.argocdClient.GetProductionAppName(appName)

	return &Scope{
		Type:        "argocd",
		PRAppName:   appName,
		ProdAppName: prodAppName,
	}, nil
}

// ListScopedProductionResources lists all XRs that belong to the production application
func (w *XRWatcher) ListScopedProductionResources(ctx context.Context, scope *Scope, gvr schema.GroupVersionResource) ([]*unstructured.Unstructured, error) {
	// List all resources of this GVR with the production app label
	listOptions := metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=%s", ArgoCDInstanceLabel, scope.ProdAppName),
	}

	list, err := w.dynamicClient.Resource(gvr).List(ctx, listOptions)
	if err != nil {
		return nil, fmt.Errorf("failed to list scoped resources for app %s: %w", scope.ProdAppName, err)
	}

	result := make([]*unstructured.Unstructured, len(list.Items))
	for i := range list.Items {
		result[i] = &list.Items[i]
	}

	return result, nil
}

// ListAllScopedProductionXRs lists all XRs across all GVRs that belong to the production application
func (w *XRWatcher) ListAllScopedProductionXRs(ctx context.Context, scope *Scope) ([]*unstructured.Unstructured, error) {
	// Discover all XRD GVRs
	gvrs, err := w.discoverXRDGVRs(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to discover XRD GVRs: %w", err)
	}

	var allXRs []*unstructured.Unstructured

	// Query each GVR with the production app label
	for _, gvr := range gvrs {
		xrs, err := w.ListScopedProductionResources(ctx, scope, gvr)
		if err != nil {
			w.logger.Error(err, "failed to list production XRs", "gvr", gvr.String())
			continue
		}
		allXRs = append(allXRs, xrs...)
	}

	return allXRs, nil
}
