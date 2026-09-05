package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	"github.com/millstonehq/crossplane-plan/pkg/argocd"
	"github.com/millstonehq/crossplane-plan/pkg/config"
	"github.com/millstonehq/crossplane-plan/pkg/detector"
	"github.com/millstonehq/crossplane-plan/pkg/differ"
	"github.com/millstonehq/crossplane-plan/pkg/formatter"
	"github.com/millstonehq/crossplane-plan/pkg/vcs"
	"github.com/millstonehq/crossplane-plan/pkg/vcs/azuredevops"
	"github.com/millstonehq/crossplane-plan/pkg/vcs/github"
	"github.com/millstonehq/crossplane-plan/pkg/watcher"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

var (
	kubeconfig             string
	detectionStrategy      string
	namePattern            string
	vcsProvider            string
	vcsGitHubRepository    string
	vcsAzureOrganization   string
	vcsAzureProjectID      string
	vcsAzureRepositoryID   string
	vcsAzureAuthMode       string
	githubToken            = os.Getenv("GITHUB_TOKEN")
	githubCredentials      = os.Getenv("GITHUB_CREDENTIALS")
	githubAppID            = os.Getenv("GITHUB_APP_ID")
	githubInstallID        = os.Getenv("GITHUB_INSTALLATION_ID")
	githubAppKeyPath       = os.Getenv("GITHUB_APP_PRIVATE_KEY_PATH")
	azurePAT               = os.Getenv("AZURE_DEVOPS_PAT")
	dryRun                 bool
	reconciliationInterval int
	configPath             string
	noStripDefaults        bool
	argocdEnabled          bool
	argocdNamespace        string
	argocdPRPrefix         string
	argocdPRSuffix         string
	outputFormat           string
)

func init() {
	flag.StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig file (optional, uses in-cluster config if not specified)")
	flag.StringVar(&detectionStrategy, "detection-strategy", "name", "PR detection strategy: name, label, or annotation")
	flag.StringVar(&namePattern, "name-pattern", "pr-{number}-*", "Name pattern for PR detection (when strategy=name)")
	flag.StringVar(&vcsProvider, "vcs-provider", "", "VCS provider: github or azure-repos")
	flag.StringVar(&vcsGitHubRepository, "vcs-github-repository", "", "GitHub repository (format: owner/repo)")
	flag.StringVar(&vcsAzureOrganization, "vcs-azure-organization", "", "Azure DevOps organization")
	flag.StringVar(&vcsAzureProjectID, "vcs-azure-project-id", "", "Azure DevOps project GUID")
	flag.StringVar(&vcsAzureRepositoryID, "vcs-azure-repository-id", "", "Azure Repos repository GUID")
	flag.StringVar(&vcsAzureAuthMode, "vcs-azure-auth-mode", "", "Azure Repos auth mode: workloadIdentity or pat")
	flag.BoolVar(&dryRun, "dry-run", false, "Dry run mode - calculate diffs but don't post to GitHub")
	flag.IntVar(&reconciliationInterval, "reconciliation-interval", 5, "Periodic reconciliation interval in minutes (0 to disable)")
	flag.StringVar(&configPath, "config", "/etc/crossplane-plan/config.yaml", "Path to config file for field stripping rules")
	flag.BoolVar(&noStripDefaults, "no-strip-defaults", false, "Disable default field stripping rules")
	flag.BoolVar(&argocdEnabled, "argocd-enabled", true, "Enable ArgoCD integration for enhanced deletion detection")
	flag.StringVar(&argocdNamespace, "argocd-namespace", "argocd", "ArgoCD namespace")
	flag.StringVar(&argocdPRPrefix, "argocd-pr-prefix", "pr-", "ArgoCD PR app name prefix (e.g., 'pr-' for 'pr-123-myapp')")
	flag.StringVar(&argocdPRSuffix, "argocd-pr-suffix", "", "ArgoCD PR app name suffix (optional)")
	flag.StringVar(&outputFormat, "output-format", "github", "Diff output format: github (GitHub-flavored markdown) or json (structured JSON for programmatic consumers)")
}

func main() {
	flag.Parse()

	// Set up logging
	zapLogger := zap.New(zap.UseDevMode(true))
	logrLogger := zapLogger.WithName("crossplane-plan")
	logger := logging.NewLogrLogger(logrLogger)

	logger.Info("Starting crossplane-plan",
		"detectionStrategy", detectionStrategy,
		"namePattern", namePattern,
		"dryRun", dryRun,
		"outputFormat", outputFormat,
	)

	// Build Kubernetes config
	cfg, err := buildKubeConfig()
	if err != nil {
		logrLogger.Error(err, "failed to build kubernetes config")
		os.Exit(1)
	}

	// Create Kubernetes clientset
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		logrLogger.Error(err, "failed to create kubernetes clientset")
		os.Exit(1)
	}

	// Load config file
	appConfig, err := config.LoadConfig(configPath)
	if err != nil {
		logrLogger.Error(err, "failed to load config")
		os.Exit(1)
	}

	// Set CLI-only fields and apply explicit provider flag overrides.
	appConfig.DetectionStrategy = detectionStrategy
	appConfig.NamePattern = namePattern
	appConfig.DryRun = dryRun
	appConfig.OutputFormat = outputFormat
	if vcsProvider != "" {
		appConfig.VCS.Provider = vcsProvider
	}
	if vcsGitHubRepository != "" {
		appConfig.VCS.GitHub.Repository = vcsGitHubRepository
	}
	if vcsAzureOrganization != "" {
		appConfig.VCS.AzureRepos.Organization = vcsAzureOrganization
	}
	if vcsAzureProjectID != "" {
		appConfig.VCS.AzureRepos.ProjectID = vcsAzureProjectID
	}
	if vcsAzureRepositoryID != "" {
		appConfig.VCS.AzureRepos.RepositoryID = vcsAzureRepositoryID
	}
	if vcsAzureAuthMode != "" {
		appConfig.VCS.AzureRepos.Auth.Mode = vcsAzureAuthMode
	}
	if !dryRun {
		if err := validateVCSConfig(appConfig.VCS); err != nil {
			logrLogger.Error(err, "invalid VCS configuration")
			os.Exit(1)
		}
	}

	// Create PR detector
	prDetector, err := createDetector(appConfig)
	if err != nil {
		logrLogger.Error(err, "failed to create PR detector")
		os.Exit(1)
	}

	// Create differ
	diffCalculator := differ.NewCalculator(cfg, logger)

	// Override stripDefaults if CLI flag is set
	if noStripDefaults {
		appConfig.Diff.StripDefaults = false
	}

	// Create and configure sanitizer
	stripRules := appConfig.GetAllStripRules()
	if len(stripRules) > 0 {
		sanitizer := differ.NewSanitizer(stripRules)
		diffCalculator.SetSanitizer(sanitizer)
		logger.Info("Field stripping enabled", "ruleCount", len(stripRules))
	} else {
		logger.Info("Field stripping disabled")
	}

	// Create formatter
	var diffFormatter formatter.Formatter
	switch appConfig.OutputFormat {
	case "github":
		diffFormatter = formatter.NewGitHubFormatter()
	case "json":
		diffFormatter = formatter.NewJSONFormatter()
	default:
		logrLogger.Error(fmt.Errorf("unknown output format: %s", appConfig.OutputFormat), "invalid --output-format", "hint", "supported values: github, json")
		os.Exit(1)
	}

	// Create VCS client (if not dry-run).
	var vcsClient vcs.Commenter
	if !dryRun {
		vcsClient, err = createVCSClient(appConfig.VCS)
		if err != nil {
			logrLogger.Error(err, "failed to create VCS client")
			os.Exit(1)
		}
		logger.Info("VCS client created successfully", "provider", appConfig.VCS.Provider)
	}

	// Create ArgoCD client (if enabled)
	var argocdClient *argocd.Client
	if argocdEnabled {
		dynamicClient, err := dynamic.NewForConfig(cfg)
		if err != nil {
			logrLogger.Error(err, "failed to create dynamic client for ArgoCD")
			os.Exit(1)
		}
		argocdClient = argocd.NewClient(
			dynamicClient,
			argocdNamespace,
			argocdPRPrefix,
			argocdPRSuffix,
			logrLogger,
		)
		logger.Info("ArgoCD client created",
			"namespace", argocdNamespace,
			"prPrefix", argocdPRPrefix,
		)
	} else {
		logger.Info("ArgoCD integration disabled")
	}

	// Create and start watcher
	xrWatcher := watcher.NewXRWatcher(
		clientset,
		prDetector,
		diffCalculator,
		diffFormatter,
		vcsClient,
		argocdClient,
		logrLogger,
		reconciliationInterval,
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown gracefully
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		logger.Info("Received shutdown signal")
		cancel()
	}()

	// Start watching
	if err := xrWatcher.Start(ctx); err != nil {
		logrLogger.Error(err, "watcher failed")
		os.Exit(1)
	}

	logger.Info("Shutting down gracefully")
}

func buildKubeConfig() (*rest.Config, error) {
	if kubeconfig != "" {
		return clientcmd.BuildConfigFromFlags("", kubeconfig)
	}
	return rest.InClusterConfig()
}

func createDetector(cfg *config.Config) (detector.Detector, error) {
	switch cfg.DetectionStrategy {
	case "name":
		return detector.NewNameDetector(cfg.NamePattern), nil
	case "label":
		return detector.NewLabelDetector(), nil
	case "annotation":
		return detector.NewAnnotationDetector(), nil
	default:
		return nil, fmt.Errorf("unknown detection strategy: %s", cfg.DetectionStrategy)
	}
}

func validateVCSConfig(cfg config.VCSConfig) error {
	switch cfg.Provider {
	case "github":
		if cfg.GitHub.Repository == "" {
			return fmt.Errorf("vcs.github.repository is required")
		}
		if githubToken == "" && githubCredentials == "" && (githubAppID == "" || githubInstallID == "" || githubAppKeyPath == "") {
			return fmt.Errorf("GitHub authentication required through supported environment variables")
		}
	case "azure-repos":
		if cfg.AzureRepos.Organization == "" || cfg.AzureRepos.ProjectID == "" || cfg.AzureRepos.RepositoryID == "" {
			return fmt.Errorf("vcs.azureRepos.organization, projectId, and repositoryId are required")
		}
		switch cfg.AzureRepos.Auth.Mode {
		case "pat":
			if azurePAT == "" {
				return fmt.Errorf("AZURE_DEVOPS_PAT is required for PAT authentication")
			}
		case "workloadIdentity":
		default:
			return fmt.Errorf("vcs.azureRepos.auth.mode must be workloadIdentity or pat")
		}
	default:
		return fmt.Errorf("unsupported vcs.provider %q", cfg.Provider)
	}
	return nil
}

func createVCSClient(cfg config.VCSConfig) (vcs.Commenter, error) {
	switch cfg.Provider {
	case "github":
		clientConfig := &github.ClientConfig{Repository: cfg.GitHub.Repository}
		if githubToken != "" {
			clientConfig.Token = githubToken
		} else if githubCredentials != "" {
			clientConfig.Credentials = githubCredentials
		} else if githubAppID != "" && githubInstallID != "" && githubAppKeyPath != "" {
			privateKey, err := os.ReadFile(githubAppKeyPath)
			if err != nil {
				return nil, fmt.Errorf("failed to read GitHub App private key: %w", err)
			}
			clientConfig.AppID = githubAppID
			clientConfig.InstallationID = githubInstallID
			clientConfig.PrivateKey = privateKey
		} else {
			return nil, fmt.Errorf("no valid GitHub authentication configured")
		}
		return github.NewClientFromConfig(clientConfig)
	case "azure-repos":
		clientConfig := azuredevops.ClientConfig{
			Organization: cfg.AzureRepos.Organization,
			ProjectID:    cfg.AzureRepos.ProjectID,
			RepositoryID: cfg.AzureRepos.RepositoryID,
			AuthMode:     cfg.AzureRepos.Auth.Mode,
		}
		if cfg.AzureRepos.Auth.Mode == "pat" {
			clientConfig.PAT = azurePAT
		} else {
			credential, err := azidentity.NewWorkloadIdentityCredential(nil)
			if err != nil {
				return nil, fmt.Errorf("create Azure workload identity credential: %w", err)
			}
			clientConfig.Credential = workloadIdentityCredential{credential: credential}
		}
		return azuredevops.NewClient(clientConfig)
	default:
		return nil, fmt.Errorf("unsupported vcs.provider %q", cfg.Provider)
	}
}

type workloadIdentityCredential struct {
	credential *azidentity.WorkloadIdentityCredential
}

func (c workloadIdentityCredential) GetToken(ctx context.Context) (azuredevops.Token, error) {
	token, err := c.credential.GetToken(ctx, policy.TokenRequestOptions{
		Scopes: []string{"499b84ac-1321-427f-aa17-267ca6975798/.default"},
	})
	if err != nil {
		return azuredevops.Token{}, err
	}
	return azuredevops.Token{Value: token.Token, ExpiresAt: token.ExpiresOn}, nil
}
