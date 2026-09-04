// Package azuredevops implements Azure Repos pull-request comments.
package azuredevops

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/millstonehq/crossplane-plan/pkg/vcs"
)

const (
	apiVersion     = "7.1"
	defaultBaseURL = "https://dev.azure.com"
	// CommentIdentifier is the immutable marker for the tool-owned preview comment.
	CommentIdentifier = "<!-- crossplane-plan:preview:v1 -->"
	activePRStatus    = "active"
	workloadIdentity  = "workloadIdentity"
	pat               = "pat"
)

var (
	// ErrConflict indicates multiple tool-owned comments or an API conflict.
	ErrConflict = errors.New("azure devops conflict")
	// ErrNotFound indicates a missing pull request or API resource.
	ErrNotFound = errors.New("azure devops resource not found")
	// ErrTerminalPR indicates a pull request that cannot receive a preview.
	ErrTerminalPR = errors.New("pull request is not active")
)

// Token is an access token returned by a workload identity credential.
type Token struct {
	Value     string
	ExpiresAt time.Time
}

// TokenCredential obtains Entra access tokens for Azure DevOps.
type TokenCredential interface {
	GetToken(context.Context) (Token, error)
}

// ClientConfig configures an Azure Repos client.
type ClientConfig struct {
	Organization string
	ProjectID    string
	RepositoryID string
	AuthMode     string
	PAT          string
	Credential   TokenCredential
}

// Client posts preview comments to one Azure Repos repository.
type Client struct {
	httpClient *http.Client
	baseURL    string
	config     ClientConfig
	tokens     *tokenCache
	locks      sync.Map
}

var _ vcs.Commenter = (*Client)(nil)

// NewClient creates an Azure Repos client using the fixed Azure DevOps Services endpoint.
func NewClient(config ClientConfig) (*Client, error) {
	if err := validateConfig(config); err != nil {
		return nil, err
	}

	return &Client{
		httpClient: http.DefaultClient,
		baseURL:    defaultBaseURL,
		config:     config,
		tokens:     newTokenCache(config),
	}, nil
}

func validateConfig(config ClientConfig) error {
	for name, value := range map[string]string{
		"organization":  config.Organization,
		"project ID":    config.ProjectID,
		"repository ID": config.RepositoryID,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("azure repos %s is required", name)
		}
	}

	switch config.AuthMode {
	case pat:
		if config.PAT == "" {
			return fmt.Errorf("azure repos PAT is required for auth mode %q", pat)
		}
	case workloadIdentity:
		if config.Credential == nil {
			return fmt.Errorf("azure repos credential is required for auth mode %q", workloadIdentity)
		}
	default:
		return fmt.Errorf("unsupported Azure Repos auth mode %q", config.AuthMode)
	}
	return nil
}

// PostComment creates or updates the unique marker-owned preview comment.
func (c *Client) PostComment(ctx context.Context, pullRequestID int, body string) error {
	if pullRequestID <= 0 {
		return fmt.Errorf("pull request ID must be positive")
	}

	lock := c.pullRequestLock(pullRequestID)
	lock.Lock()
	defer lock.Unlock()

	pr, err := c.getPullRequest(ctx, pullRequestID)
	if err != nil {
		return fmt.Errorf("get pull request %d: %w", pullRequestID, err)
	}
	if !strings.EqualFold(pr.Status, activePRStatus) {
		return fmt.Errorf("pull request %d: %w (%s)", pullRequestID, ErrTerminalPR, pr.Status)
	}

	threads, err := c.listThreads(ctx, pullRequestID)
	if err != nil {
		return fmt.Errorf("list pull request %d threads: %w", pullRequestID, err)
	}
	managed, err := findManagedComment(threads)
	if err != nil {
		return fmt.Errorf("find preview comment for pull request %d: %w", pullRequestID, err)
	}
	commentBody := CommentIdentifier + "\n\n" + body
	if managed != nil {
		if err := c.updateComment(ctx, pullRequestID, managed.threadID, managed.commentID, commentBody); err != nil {
			return fmt.Errorf("update preview comment for pull request %d: %w", pullRequestID, err)
		}
		return nil
	}

	if err := c.createComment(ctx, pullRequestID, commentBody); err != nil {
		// POST can succeed server-side while its response is lost. Re-list before
		// reporting failure or considering another create, never blindly retry POST.
		if isRetryable(err) {
			threads, listErr := c.listThreads(ctx, pullRequestID)
			if listErr == nil {
				managed, findErr := findManagedComment(threads)
				if findErr == nil && managed != nil {
					if updateErr := c.updateComment(ctx, pullRequestID, managed.threadID, managed.commentID, commentBody); updateErr == nil {
						return nil
					}
				}
			}
		}
		return fmt.Errorf("create preview comment for pull request %d: %w", pullRequestID, err)
	}
	return nil
}

// DeleteComment removes the marker-owned preview comment. Missing comments succeed.
func (c *Client) DeleteComment(ctx context.Context, pullRequestID int) error {
	if pullRequestID <= 0 {
		return fmt.Errorf("pull request ID must be positive")
	}

	lock := c.pullRequestLock(pullRequestID)
	lock.Lock()
	defer lock.Unlock()

	threads, err := c.listThreads(ctx, pullRequestID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return fmt.Errorf("list pull request %d threads: %w", pullRequestID, err)
	}
	managed, err := findManagedComment(threads)
	if err != nil {
		return fmt.Errorf("find preview comment for pull request %d: %w", pullRequestID, err)
	}
	if managed == nil {
		return nil
	}
	if err := c.deleteComment(ctx, pullRequestID, managed.threadID, managed.commentID); err != nil && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("delete preview comment for pull request %d: %w", pullRequestID, err)
	}
	return nil
}

type pullRequest struct {
	Status string `json:"status"`
}

type threadList struct {
	Value []thread `json:"value"`
}

type thread struct {
	ID        int       `json:"id"`
	IsDeleted bool      `json:"isDeleted"`
	Comments  []comment `json:"comments"`
}

type comment struct {
	ID              int    `json:"id"`
	ParentCommentID int    `json:"parentCommentId"`
	Content         string `json:"content"`
	CommentType     int    `json:"commentType,omitempty"`
	IsDeleted       bool   `json:"isDeleted"`
}

type managedComment struct {
	threadID  int
	commentID int
}

func findManagedComment(threads []thread) (*managedComment, error) {
	var found *managedComment
	for _, thread := range threads {
		if thread.IsDeleted {
			continue
		}
		for _, comment := range thread.Comments {
			if !comment.IsDeleted && comment.ParentCommentID == 0 && strings.Contains(comment.Content, CommentIdentifier) {
				if found != nil {
					return nil, ErrConflict
				}
				candidate := &managedComment{threadID: thread.ID, commentID: comment.ID}
				found = candidate
			}
		}
	}
	return found, nil
}

func (c *Client) getPullRequest(ctx context.Context, pullRequestID int) (pullRequest, error) {
	var pr pullRequest
	path := fmt.Sprintf("/%s/%s/_apis/git/repositories/%s/pullRequests/%d?api-version=%s",
		url.PathEscape(c.config.Organization), url.PathEscape(c.config.ProjectID),
		url.PathEscape(c.config.RepositoryID), pullRequestID, apiVersion)
	if err := c.request(ctx, http.MethodGet, path, nil, &pr, true); err != nil {
		return pullRequest{}, err
	}
	return pr, nil
}

func (c *Client) listThreads(ctx context.Context, pullRequestID int) ([]thread, error) {
	var result threadList
	path := fmt.Sprintf("/%s/%s/_apis/git/repositories/%s/pullRequests/%d/threads?api-version=%s",
		url.PathEscape(c.config.Organization), url.PathEscape(c.config.ProjectID),
		url.PathEscape(c.config.RepositoryID), pullRequestID, apiVersion)
	if err := c.request(ctx, http.MethodGet, path, nil, &result, true); err != nil {
		return nil, err
	}
	return result.Value, nil
}

func (c *Client) createComment(ctx context.Context, pullRequestID int, content string) error {
	payload := map[string]any{
		"comments": []comment{{ParentCommentID: 0, Content: content, CommentType: 1}},
		"status":   1,
	}
	path := fmt.Sprintf("/%s/%s/_apis/git/repositories/%s/pullRequests/%d/threads?api-version=%s",
		url.PathEscape(c.config.Organization), url.PathEscape(c.config.ProjectID),
		url.PathEscape(c.config.RepositoryID), pullRequestID, apiVersion)
	return c.request(ctx, http.MethodPost, path, payload, nil, true)
}

func (c *Client) updateComment(ctx context.Context, pullRequestID, threadID, commentID int, content string) error {
	payload := comment{ID: commentID, Content: content}
	path := fmt.Sprintf("/%s/%s/_apis/git/repositories/%s/pullRequests/%d/threads/%d/comments/%d?api-version=%s",
		url.PathEscape(c.config.Organization), url.PathEscape(c.config.ProjectID),
		url.PathEscape(c.config.RepositoryID), pullRequestID, threadID, commentID, apiVersion)
	return c.request(ctx, http.MethodPatch, path, payload, nil, true)
}

func (c *Client) deleteComment(ctx context.Context, pullRequestID, threadID, commentID int) error {
	path := fmt.Sprintf("/%s/%s/_apis/git/repositories/%s/pullRequests/%d/threads/%d/comments/%d?api-version=%s",
		url.PathEscape(c.config.Organization), url.PathEscape(c.config.ProjectID),
		url.PathEscape(c.config.RepositoryID), pullRequestID, threadID, commentID, apiVersion)
	return c.request(ctx, http.MethodDelete, path, nil, nil, true)
}

func (c *Client) request(ctx context.Context, method, path string, payload any, result any, retryUnauthorized bool) error {
	data, err := json.Marshal(payload)
	if payload != nil && err != nil {
		return fmt.Errorf("encode request: %w", err)
	}

	maxAttempts := 1
	if retryUnauthorized {
		maxAttempts = 2
	}
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		token, tokenErr := c.tokens.get(ctx)
		if tokenErr != nil {
			return fmt.Errorf("get Azure DevOps token: %w", tokenErr)
		}
		req, reqErr := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(data))
		if reqErr != nil {
			return fmt.Errorf("create request: %w", reqErr)
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", "application/json")
		if c.config.AuthMode == pat {
			basic := base64.StdEncoding.EncodeToString([]byte(":" + token))
			req.Header.Set("Authorization", "Basic "+basic)
		} else {
			req.Header.Set("Authorization", "Bearer "+token)
		}

		resp, doErr := c.httpClient.Do(req)
		if doErr != nil {
			lastErr = &APIError{Operation: method, Category: CategoryTransient, Cause: doErr}
			if attempt == 0 && method != http.MethodPost {
				continue
			}
			break
		}
		responseErr := readResponse(resp, method, result)
		if responseErr == nil {
			return nil
		}
		lastErr = responseErr
		var apiErr *APIError
		if errors.As(responseErr, &apiErr) && apiErr.StatusCode == http.StatusUnauthorized && attempt == 0 {
			c.tokens.invalidate()
			continue
		}
		if errors.As(responseErr, &apiErr) && retryableStatus(apiErr.StatusCode) && attempt == 0 && method != http.MethodPost {
			continue
		}
		break
	}
	return lastErr
}

func readResponse(resp *http.Response, operation string, result any) error {
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		category := CategoryProtocol
		switch resp.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			category = CategoryPermission
		case http.StatusNotFound:
			category = CategoryNotFound
		case http.StatusConflict:
			category = CategoryConflict
		case http.StatusTooManyRequests:
			category = CategoryTransient
		default:
			if resp.StatusCode >= 500 {
				category = CategoryTransient
			}
		}
		return &APIError{Operation: operation, StatusCode: resp.StatusCode, Category: category}
	}
	if result == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(result); err != nil && !errors.Is(err, io.EOF) {
		return &APIError{Operation: operation, Category: CategoryProtocol, Cause: fmt.Errorf("decode response: %w", err)}
	}
	return nil
}

func retryableStatus(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
}

func isRetryable(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Category == CategoryTransient
}

// Category classifies provider failures without exposing provider response types.
type Category string

const (
	CategoryTransient  Category = "transient"
	CategoryPermission Category = "permission"
	CategoryValidation Category = "validation"
	CategoryNotFound   Category = "not-found"
	CategoryConflict   Category = "conflict"
	CategoryProtocol   Category = "protocol"
)

// APIError describes a classified Azure DevOps request failure.
type APIError struct {
	Operation  string
	StatusCode int
	Category   Category
	Cause      error
}

func (e *APIError) Error() string {
	if e.StatusCode != 0 {
		return fmt.Sprintf("Azure DevOps API %s error (%s, status %d)", e.Operation, e.Category, e.StatusCode)
	}
	return fmt.Sprintf("Azure DevOps API %s error (%s): %v", e.Operation, e.Category, e.Cause)
}

func (e *APIError) Unwrap() error {
	switch e.Category {
	case CategoryNotFound:
		return ErrNotFound
	case CategoryConflict:
		return ErrConflict
	default:
		return e.Cause
	}
}

type tokenCache struct {
	mu         sync.Mutex
	pat        string
	credential TokenCredential
	token      Token
}

func newTokenCache(config ClientConfig) *tokenCache {
	return &tokenCache{pat: config.PAT, credential: config.Credential}
}

func (t *tokenCache) get(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.pat != "" {
		return t.pat, nil
	}
	if t.token.Value != "" && time.Until(t.token.ExpiresAt) > time.Minute {
		return t.token.Value, nil
	}
	token, err := t.credential.GetToken(ctx)
	if err != nil {
		return "", err
	}
	if token.Value == "" {
		return "", fmt.Errorf("Azure DevOps credential returned an empty token")
	}
	t.token = token
	return token.Value, nil
}

func (t *tokenCache) invalidate() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.token = Token{}
}

func (c *Client) pullRequestLock(id int) *sync.Mutex {
	value, _ := c.locks.LoadOrStore(id, &sync.Mutex{})
	return value.(*sync.Mutex)
}
