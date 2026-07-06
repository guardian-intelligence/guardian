package main

import (
	"context"
	"strconv"
	"time"

	"log/slog"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// commenter owns the per-PR comment: one comment per PR (created once,
// PATCHed thereafter), rendered from the job/demand tables whenever a job
// event marks the PR dirty. Fully decoupled from delivery processing — a
// GitHub comment outage backs off in pr_comment_state and can never
// back-pressure webhook ingest.
type commenter struct {
	st     *pgStore
	gh     *githubClient
	cfg    config
	tracer trace.Tracer
}

func (c *commenter) run(ctx context.Context) {
	ticker := time.NewTicker(c.cfg.commentInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		c.tick(ctx)
	}
}

func (c *commenter) tick(ctx context.Context) {
	rows, err := c.st.ListDirtyPRComments(ctx, c.cfg.workerBatchSize)
	if err != nil {
		slog.Error("comment: list dirty", "err", err)
		return
	}
	for _, row := range rows {
		c.sync(ctx, row)
	}
}

func (c *commenter) sync(ctx context.Context, row prCommentRow) {
	ctx, span := c.tracer.Start(ctx, "comment.sync", trace.WithAttributes(
		attribute.String("repo", row.RepositoryFullName),
		attribute.Int64("pr", row.PRNumber),
	))
	defer span.End()

	jobs, err := c.st.ListPRJobs(ctx, row.ProviderRepositoryID, row.PRNumber)
	if err != nil {
		slog.Error("comment: list jobs", "repo", row.RepositoryFullName, "pr", row.PRNumber, "err", err)
		return
	}
	if len(jobs) == 0 && row.ProviderCommentID == 0 {
		// Nothing to say and nothing posted yet.
		if err := c.st.MarkPRCommentClean(ctx, row.ProviderRepositoryID, row.PRNumber); err != nil {
			slog.Error("comment: mark clean", "repo", row.RepositoryFullName, "pr", row.PRNumber, "err", err)
		}
		return
	}

	body := renderComment(jobs)
	hash := renderedSHA256(body)
	if hash == row.LastRenderedSHA256 && row.ProviderCommentID != 0 {
		// Rendered content unchanged: skip the API round-trip.
		if err := c.st.MarkPRCommentClean(ctx, row.ProviderRepositoryID, row.PRNumber); err != nil {
			slog.Error("comment: mark clean", "repo", row.RepositoryFullName, "pr", row.PRNumber, "err", err)
		}
		return
	}

	commentID := row.ProviderCommentID
	var postErr error
	if commentID == 0 {
		commentID, postErr = c.gh.createIssueComment(ctx, row.RepositoryFullName, row.PRNumber, body)
	} else {
		postErr = c.gh.updateIssueComment(ctx, row.RepositoryFullName, commentID, body)
	}
	if postErr != nil {
		next := time.Now().Add(retryDelay(row.AttemptCount + 1))
		if err := c.st.DeferPRComment(ctx, row.ProviderRepositoryID, row.PRNumber, next); err != nil {
			slog.Error("comment: defer", "repo", row.RepositoryFullName, "pr", row.PRNumber, "err", err)
		}
		emitEvent(ctx, evCommentFailed, eventAttrs{
			Repo:   row.RepositoryFullName,
			Result: "failed",
			Reason: postErr.Error(),
		})
		return
	}
	if err := c.st.MarkPRCommentPosted(ctx, row.ProviderRepositoryID, row.PRNumber, commentID, hash); err != nil {
		slog.Error("comment: mark posted", "repo", row.RepositoryFullName, "pr", row.PRNumber, "err", err)
		return
	}
	emitEvent(ctx, evCommentPosted, eventAttrs{
		Repo:   row.RepositoryFullName,
		Result: "succeeded",
		Reason: "pr:" + strconv.FormatInt(row.PRNumber, 10),
	})
}
