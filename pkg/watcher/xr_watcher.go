package watcher

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/go-logr/logr"
	"github.com/millstonehq/crossplane-plan/pkg/argocd"
	"github.com/millstonehq/crossplane-plan/pkg/detector"
	"github.com/millstonehq/crossplane-plan/pkg/differ"
	"github.com/millstonehq/crossplane-plan/pkg/formatter"
	"github.com/millstonehq/crossplane-plan/pkg/vcs/github"
	"github.com/millstonehq/crossplane-plan/pkg/workqueue"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
)

// XRWatcher watches Crossplane Composite Resources and posts diffs to GitHub
type XRWatcher struct {
	clientset              *kubernetes.Clientset
	dynamicClient          dynamic.Interface
	detector               detector.Detector
	differ                 *differ.Calculator
	formatter              formatter.Formatter
	vcsClient              *github.Client
	argocdClient           *argocd.Client
	logger                 logr.Logger
	processedXRs           map[string]string // name -> resource version
	reconciliationInterval int               // minutes
	workQueue              *workqueue.PRWorkQueue
	cfg                    *rest.Config
}

// NewXRWatcher creates a new XRWatcher
func NewXRWatcher(
	clientset *kubernetes.Clientset,
	detector detector.Detector,
	differ *differ.Calculator,
	formatter formatter.Formatter,
	vcsClient *github.Client,
	argocdClient *argocd.Client,
	logger logr.Logger,
	reconciliationInterval int,
) *XRWatcher {
	// Create dynamic client from the same config
	cfg, err := rest.InClusterConfig()
	if err != nil {
		// Try building from kubeconfig if in-cluster fails
		panic(fmt.Sprintf("failed to get kubernetes config: %v", err))
	}

	dynamicClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		panic(fmt.Sprintf("failed to create dynamic client: %v", err))
	}

	watcher := &XRWatcher{
		clientset:              clientset,
		dynamicClient:          dynamicClient,
		detector:               detector,
		differ:                 differ,
		formatter:              formatter,
		vcsClient:              vcsClient,
		argocdClient:           argocdClient,
		logger:                 logger,
		processedXRs:           make(map[string]string),
		reconciliationInterval: reconciliationInterval,
		cfg:                    cfg,
	}

	// Create work queue with 5-second debounce
	watcher.workQueue = workqueue.NewPRWorkQueue(watcher, logger, 5*time.Second)

	return watcher
}

// Start begins watching Crossplane XRs with leader election
func (w *XRWatcher) Start(ctx context.Context) error {
	w.logger.Info("Starting XR watcher with leader election")

	// Get pod identity for leader election
	podName := os.Getenv("POD_NAME")
	if podName == "" {
		podName = "crossplane-plan-unknown"
		w.logger.Info("POD_NAME not set, using default", "identity", podName)
	}

	podNamespace := os.Getenv("POD_NAMESPACE")
	if podNamespace == "" {
		podNamespace = "crossplane-system"
		w.logger.Info("POD_NAMESPACE not set, using default", "namespace", podNamespace)
	}

	// Create leader election lock
	lock := &resourcelock.LeaseLock{
		LeaseMeta: metav1.ObjectMeta{
			Name:      "crossplane-plan-leader",
			Namespace: podNamespace,
		},
		Client: w.clientset.CoordinationV1(),
		LockConfig: resourcelock.ResourceLockConfig{
			Identity: podName,
		},
	}

	// Run leader election
	leaderelection.RunOrDie(ctx, leaderelection.LeaderElectionConfig{
		Lock:            lock,
		ReleaseOnCancel: true,
		LeaseDuration:   15 * time.Second,
		RenewDeadline:   10 * time.Second,
		RetryPeriod:     2 * time.Second,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(ctx context.Context) {
				w.logger.Info("Acquired leadership, starting watchers")
				if err := w.run(ctx); err != nil {
					w.logger.Error(err, "Failed to run watchers")
				}
			},
			OnStoppedLeading: func() {
				w.logger.Info("Lost leadership, stopping")
			},
			OnNewLeader: func(identity string) {
				if identity != podName {
					w.logger.Info("New leader elected", "leader", identity)
				}
			},
		},
	})

	return nil
}

// run contains the main watcher logic (called by leader election)
func (w *XRWatcher) run(ctx context.Context) error {
	// Discover Crossplane XRD GVRs
	gvrs, err := w.discoverXRDGVRs(ctx)
	if err != nil {
		return fmt.Errorf("failed to discover XRDs: %w", err)
	}

	w.logger.Info("Discovered XRDs", "count", len(gvrs))

	// Initial reconciliation - process existing PR XRs
	w.logger.Info("Starting initial reconciliation of existing PR XRs")
	for _, gvr := range gvrs {
		if err := w.reconcileExistingXRs(ctx, gvr); err != nil {
			w.logger.Error(err, "failed initial reconciliation", "gvr", gvr.String())
			// Don't fail startup, just log and continue
		}
	}
	w.logger.Info("Initial reconciliation complete")

	// Watch each GVR for changes
	for _, gvr := range gvrs {
		go w.watchGVR(ctx, gvr)
	}

	// Start periodic reconciliation if enabled
	if w.reconciliationInterval > 0 {
		ticker := time.NewTicker(time.Duration(w.reconciliationInterval) * time.Minute)
		defer ticker.Stop()

		w.logger.Info("Starting periodic reconciliation", "interval", fmt.Sprintf("%dm", w.reconciliationInterval))

		go func() {
			for {
				select {
				case <-ticker.C:
					w.logger.Info("Running periodic reconciliation")
					for _, gvr := range gvrs {
						if err := w.reconcileExistingXRs(ctx, gvr); err != nil {
							w.logger.Error(err, "periodic reconciliation failed", "gvr", gvr.String())
						}
					}
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	// Block until context is cancelled
	<-ctx.Done()
	return nil
}

// discoverXRDGVRs discovers all Crossplane XRDs in the cluster
func (w *XRWatcher) discoverXRDGVRs(ctx context.Context) ([]schema.GroupVersionResource, error) {
	// XRDs are defined by apiextensions.crossplane.io/v1 CompositeResourceDefinition
	xrdGVR := schema.GroupVersionResource{
		Group:    "apiextensions.crossplane.io",
		Version:  "v1",
		Resource: "compositeresourcedefinitions",
	}

	xrds, err := w.dynamicClient.Resource(xrdGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list XRDs: %w", err)
	}

	var gvrs []schema.GroupVersionResource
	for _, xrd := range xrds.Items {
		// Extract group from spec.group
		group, found, err := unstructured.NestedString(xrd.Object, "spec", "group")
		if err != nil || !found {
			w.logger.Error(err, "failed to get group from XRD", "name", xrd.GetName())
			continue
		}

		// Extract plural from spec.names.plural
		plural, found, err := unstructured.NestedString(xrd.Object, "spec", "names", "plural")
		if err != nil || !found {
			w.logger.Error(err, "failed to get plural from XRD", "name", xrd.GetName())
			continue
		}

		// Get served versions from spec.versions
		versions, found, err := unstructured.NestedSlice(xrd.Object, "spec", "versions")
		if err != nil || !found {
			w.logger.Error(err, "failed to get versions from XRD", "name", xrd.GetName())
			continue
		}

		// Find first served+referenceable version
		for _, v := range versions {
			versionMap, ok := v.(map[string]interface{})
			if !ok {
				continue
			}

			served, _, _ := unstructured.NestedBool(versionMap, "served")
			referenceable, _, _ := unstructured.NestedBool(versionMap, "referenceable")
			versionName, _, _ := unstructured.NestedString(versionMap, "name")

			if served && referenceable && versionName != "" {
				gvrs = append(gvrs, schema.GroupVersionResource{
					Group:    group,
					Version:  versionName,
					Resource: plural,
				})
				break
			}
		}
	}

	return gvrs, nil
}

// reconcileExistingXRs performs initial reconciliation of existing XRs for a GVR
func (w *XRWatcher) reconcileExistingXRs(ctx context.Context, gvr schema.GroupVersionResource) error {
	list, err := w.dynamicClient.Resource(gvr).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list resources: %w", err)
	}

	w.logger.Info("Checking for existing PR XRs", "gvr", gvr.String(), "totalCount", len(list.Items))

	// Group XRs by PR number
	prXRs := make(map[int][]*unstructured.Unstructured)
	for _, item := range list.Items {
		xr := item.DeepCopy()

		// Only process PR XRs
		prNumber := w.detector.DetectPR(xr)
		if prNumber == 0 {
			continue
		}

		prXRs[prNumber] = append(prXRs[prNumber], xr)
	}

	// Process each PR's XRs as a batch
	for prNumber, xrs := range prXRs {
		w.logger.Info("Reconciling PR XRs", "prNumber", prNumber, "count", len(xrs))
		if err := w.handlePRBatch(ctx, prNumber, xrs); err != nil {
			w.logger.Error(err, "failed to process PR batch", "prNumber", prNumber)
			// Continue with other PRs
		}
	}

	if len(prXRs) > 0 {
		w.logger.Info("Reconciled existing PR XRs", "gvr", gvr.String(), "prCount", len(prXRs))
	}

	return nil
}

// watchGVR watches a specific GVR for changes
func (w *XRWatcher) watchGVR(ctx context.Context, gvr schema.GroupVersionResource) {
	w.logger.Info("Watching GVR", "gvr", gvr.String())

	for {
		select {
		case <-ctx.Done():
			return
		default:
			if err := w.watchGVROnce(ctx, gvr); err != nil {
				w.logger.Error(err, "watch failed, retrying in 5s", "gvr", gvr.String())
				time.Sleep(5 * time.Second)
			}
		}
	}
}

// watchGVROnce performs a single watch operation
func (w *XRWatcher) watchGVROnce(ctx context.Context, gvr schema.GroupVersionResource) error {
	watcher, err := w.dynamicClient.Resource(gvr).Watch(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to create watcher: %w", err)
	}
	defer watcher.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-watcher.ResultChan():
			if !ok {
				return fmt.Errorf("watch channel closed")
			}

			if event.Type == watch.Error {
				w.logger.Error(nil, "watch error event", "gvr", gvr.String())
				continue
			}

			xr, ok := event.Object.(*unstructured.Unstructured)
			if !ok {
				w.logger.Error(nil, "unexpected object type", "gvr", gvr.String())
				continue
			}

			w.handleXREvent(ctx, event.Type, xr)
		}
	}
}

// handlePRBatch processes all XRs for a single PR and posts one combined comment
func (w *XRWatcher) handlePRBatch(ctx context.Context, prNumber int, xrs []*unstructured.Unstructured) error {
	if len(xrs) == 0 {
		return nil
	}

	results := make(map[string]*differ.DiffResult)
	var argocdDiff *argocd.AppDiff
	var scope *Scope

	// 1. Discover scope from first PR XR (all should have same ArgoCD app label)
	if w.argocdClient != nil {
		discoveredScope, err := w.DiscoverScope(xrs[0])
		if err != nil {
			w.logger.Error(err, "failed to discover scope, falling back to legacy detection",
				"xr", xrs[0].GetName())
			// Continue without ArgoCD integration (degraded mode)
		} else {
			scope = discoveredScope
			w.logger.Info("Discovered scope",
				"prApp", scope.PRAppName,
				"prodApp", scope.ProdAppName)
		}
	}

	// 2. Run crossplane-diff for composition preview (existing behavior)
	for _, xr := range xrs {
		name := xr.GetName()
		namespace := xr.GetNamespace()

		w.logger.Info("Processing XR in batch",
			"name", name,
			"namespace", namespace,
			"prNumber", prNumber,
		)

		// Clone the XR and rename it to the production name
		baseName := w.detector.GetBaseName(xr)
		xrForDiff := xr.DeepCopy()
		xrForDiff.SetName(baseName)

		// Clear immutable metadata fields
		xrForDiff.SetUID("")
		xrForDiff.SetResourceVersion("")
		xrForDiff.SetGeneration(0)
		xrForDiff.SetCreationTimestamp(metav1.Time{})
		xrForDiff.SetManagedFields(nil)

		w.logger.Info("Comparing PR XR against production",
			"prName", name,
			"productionName", baseName,
		)

		// Calculate diff
		diff, err := w.differ.CalculateDiff(ctx, xrForDiff)
		if err != nil {
			w.logger.Error(err, "failed to calculate diff", "name", name)
			continue
		}

		// Store result using original XR name as key
		results[name] = diff
	}

	// 3. NEW: ArgoCD diff for deletions + bare resources
	if w.argocdClient != nil && scope != nil {
		appDiff, err := w.argocdClient.GetAppDiff(ctx, scope.PRAppName, scope.ProdAppName)
		if err != nil {
			if errors.Is(err, argocd.ErrNotFound) {
				w.logger.Info("ArgoCD diff unavailable, using fallback deletion detection",
					"prApp", scope.PRAppName,
					"prodApp", scope.ProdAppName)
				// Fall back to legacy deletion detection
				if err := w.detectDeletions(ctx, prNumber, xrs, results); err != nil {
					w.logger.Error(err, "legacy deletion detection failed", "prNumber", prNumber)
				}
			} else {
				w.logger.Error(err, "ArgoCD diff failed, using fallback",
					"prApp", scope.PRAppName,
					"prodApp", scope.ProdAppName)
				// Continue with fallback
				if err := w.detectDeletions(ctx, prNumber, xrs, results); err != nil {
					w.logger.Error(err, "legacy deletion detection failed", "prNumber", prNumber)
				}
			}
		} else {
			// Successfully got ArgoCD diff
			argocdDiff = appDiff
			w.logger.Info("ArgoCD diff complete",
				"additions", len(appDiff.Additions),
				"modifications", len(appDiff.Modifications),
				"deletions", len(appDiff.Deletions))

			// Add ArgoCD deletions to results
			for _, deletion := range appDiff.Deletions {
				key := fmt.Sprintf("DELETED-%s", deletion.Name)
				results[key] = &differ.DiffResult{
					HasChanges: true,
					Summary:    fmt.Sprintf("⚠️ %s will be **DELETED** (ArgoCD)", deletion.GVK.Kind),
					RawDiff:    deletion.RawDiff,
				}
			}
		}
	} else {
		// No ArgoCD client or scope - use legacy deletion detection
		if err := w.detectDeletions(ctx, prNumber, xrs, results); err != nil {
			w.logger.Error(err, "failed to detect deletions", "prNumber", prNumber)
		}
	}

	// If no results, nothing to post
	if len(results) == 0 {
		return nil
	}

	// Format combined comment
	var comment string
	if len(results) == 1 && argocdDiff == nil {
		// Single XR with no ArgoCD diff - use simple format
		for _, diff := range results {
			comment = w.formatter.FormatDiff(xrs[0], diff)
		}
	} else {
		// Multiple XRs or ArgoCD diff present - use combined format
		comment = w.formatter.FormatMultipleDiffs(results, argocdDiff)
	}

	// Post to GitHub
	if w.vcsClient != nil {
		if err := w.vcsClient.PostComment(ctx, prNumber, comment); err != nil {
			return fmt.Errorf("failed to post GitHub comment: %w", err)
		}
		w.logger.Info("Posted GitHub comment", "prNumber", prNumber, "resourceCount", len(results))
	} else {
		// Dry-run mode: log that we would have posted, and emit the
		// rendered output on stdout (separate from the logger, which writes
		// to stderr) so it can be inspected or piped to another tool
		// regardless of which formatter produced it.
		w.logger.Info("Dry-run: would post comment", "prNumber", prNumber, "resourceCount", len(results))
		fmt.Println(comment)
	}

	return nil
}

// ProcessPR implements the workqueue.PRProcessor interface
// This is called by the work queue after debouncing
func (w *XRWatcher) ProcessPR(ctx context.Context, prNumber int) error {
	w.logger.Info("Processing all resources for PR", "prNumber", prNumber)

	// Query all XRs for this PR across all GVRs
	xrs, err := w.findAllPRResources(ctx, prNumber)
	if err != nil {
		return fmt.Errorf("failed to find PR resources: %w", err)
	}

	if len(xrs) == 0 {
		w.logger.Info("No resources found for PR", "prNumber", prNumber)
		return nil
	}

	w.logger.Info("Found resources for PR", "prNumber", prNumber, "count", len(xrs))

	// Process all XRs as a batch
	return w.handlePRBatch(ctx, prNumber, xrs)
}

// findAllPRResources queries all XRs matching the given PR number
func (w *XRWatcher) findAllPRResources(ctx context.Context, prNumber int) ([]*unstructured.Unstructured, error) {
	gvrs, err := w.discoverXRDGVRs(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to discover XRDs: %w", err)
	}

	var allXRs []*unstructured.Unstructured
	for _, gvr := range gvrs {
		list, err := w.dynamicClient.Resource(gvr).List(ctx, metav1.ListOptions{})
		if err != nil {
			w.logger.Error(err, "failed to list resources", "gvr", gvr.String())
			continue
		}

		for _, item := range list.Items {
			xr := item.DeepCopy()
			if w.detector.DetectPR(xr) == prNumber {
				allXRs = append(allXRs, xr)
			}
		}
	}

	return allXRs, nil
}

// detectDeletions finds production resources that will be deleted (no PR equivalent exists)
func (w *XRWatcher) detectDeletions(ctx context.Context, prNumber int, prResources []*unstructured.Unstructured, results map[string]*differ.DiffResult) error {
	// Build a map of PR resource base names for quick lookup
	prBaseNames := make(map[string]bool)
	prGVKs := make(map[schema.GroupVersionKind]bool)

	for _, prXR := range prResources {
		baseName := w.detector.GetBaseName(prXR)
		prBaseNames[baseName] = true
		prGVKs[prXR.GroupVersionKind()] = true
	}

	// If no PR resources, nothing to compare against
	if len(prBaseNames) == 0 {
		return nil
	}

	// Get all GVRs we're watching
	gvrs, err := w.discoverXRDGVRs(ctx)
	if err != nil {
		return fmt.Errorf("failed to discover XRDs: %w", err)
	}

	// Find all production resources (non-PR resources)
	for _, gvr := range gvrs {
		list, err := w.dynamicClient.Resource(gvr).List(ctx, metav1.ListOptions{})
		if err != nil {
			w.logger.Error(err, "failed to list production resources", "gvr", gvr.String())
			continue
		}

		for _, item := range list.Items {
			prodXR := item.DeepCopy()

			// Skip if this is a PR resource
			if w.detector.DetectPR(prodXR) != 0 {
				continue
			}

			// Skip if this GVK is not in the PR (PR doesn't touch this resource type)
			if !prGVKs[prodXR.GroupVersionKind()] {
				continue
			}

			prodName := prodXR.GetName()

			// Check if there's a corresponding PR resource
			if !prBaseNames[prodName] {
				// This production resource will be deleted!
				w.logger.Info("Detected deletion",
					"resource", prodName,
					"gvk", prodXR.GroupVersionKind().String(),
					"prNumber", prNumber,
				)

				// Create a deletion diff result
				deletionDiff := &differ.DiffResult{
					XR:         prodXR,
					HasChanges: true,
					Summary:    "⚠️  Resource will be **DELETED**",
					RawDiff:    fmt.Sprintf("Resource %s/%s will be deleted", prodXR.GetKind(), prodName),
					ManagedResources: []differ.ManagedResourceState{},
					StrippedFields:   []differ.StrippedField{},
				}

				// Use a special key format for deletions to distinguish from modifications
				deletionKey := fmt.Sprintf("DELETED-%s", prodName)
				results[deletionKey] = deletionDiff
			}
		}
	}

	return nil
}

// handleXREvent processes an XR event by enqueueing it for batch processing
func (w *XRWatcher) handleXREvent(ctx context.Context, eventType watch.EventType, xr *unstructured.Unstructured) {
	name := xr.GetName()
	namespace := xr.GetNamespace()

	// Detect PR number
	prNumber := w.detector.DetectPR(xr)
	if prNumber == 0 {
		// Not a PR preview XR, skip
		return
	}

	w.logger.Info("Processing XR event",
		"type", eventType,
		"name", name,
		"namespace", namespace,
		"prNumber", prNumber,
	)

	// Enqueue for batch processing (debounced)
	w.workQueue.Enqueue(ctx, prNumber)
}
