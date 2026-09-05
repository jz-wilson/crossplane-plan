package vcs

import "context"

// Commenter posts and removes the tool-owned preview comment for a pull request.
type Commenter interface {
	PostComment(ctx context.Context, pullRequestID int, body string) error
	DeleteComment(ctx context.Context, pullRequestID int) error
}
