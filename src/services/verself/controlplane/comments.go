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
	// Tick work runs on a non-cancelable child: shutdown stops the loop
	// between ticks, but an in-flight sync finishes (bounded by the GitHub
	// client timeout) instead of leaving a posted comment unrecorded.
	work := context.WithoutCancel(ctx)
	ticker := time.NewTicker(c.cfg.commentInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		c.tick(work)
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
		if err := c.st.MarkPRCommentClean(ctx, row.ProviderRepositoryID, row.PRNumber, row.UpdatedAt); err != nil {
			slog.Error("comment: mark clean", "repo", row.RepositoryFullName, "pr", row.PRNumber, "err", err)
		}
		return
	}

	body := renderComment(jobs)
	hash := renderedSHA256(body)
	if hash == row.LastRenderedSHA256 && row.ProviderCommentID != 0 {
		// Rendered content unchanged: skip the API round-trip.
		if err := c.st.MarkPRCommentClean(ctx, row.ProviderRepositoryID, row.PRNumber, row.UpdatedAt); err != nil {
			slog.Error("comment: mark clean", "repo", row.RepositoryFullName, "pr", row.PRNumber, "err", err)
		}
		return
	}

	commentID := row.ProviderCommentID
	var postErr error
	if commentID == 0 {
		commentID, postErr = c.createOrAdoptComment(ctx, row, body)
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
	if err := c.st.MarkPRCommentPosted(ctx, row.ProviderRepositoryID, row.PRNumber, commentID, hash, row.UpdatedAt); err != nil {
		slog.Error("comment: mark posted", "repo", row.RepositoryFullName, "pr", row.PRNumber, "err", err)
		return
	}
	emitEvent(ctx, evCommentPosted, eventAttrs{
		Repo:   row.RepositoryFullName,
		Result: "succeeded",
		Reason: "pr:" + strconv.FormatInt(row.PRNumber, 10),
	})
}

// createOrAdoptComment handles the no-known-comment case. A comment may
// already exist whose id we lost (createIssueComment succeeded but the
// MarkPRCommentPosted write failed), so before creating we adopt an existing
// bot-authored comment carrying our marker — otherwise every such partial
// failure would grow a second comment. The bot-author filter keeps a
// marker-quoting comment from a human (or a hostile fork PR description
// pasted into a comment) from being adopted and overwritten.
func (c *commenter) createOrAdoptComment(ctx context.Context, row prCommentRow, body string) (int64, error) {
	existing, err := c.gh.findMarkerComment(ctx, row.RepositoryFullName, row.PRNumber, commentMarker)
	if err != nil {
		return 0, err
	}
	if existing != 0 {
		return existing, c.gh.updateIssueComment(ctx, row.RepositoryFullName, existing, body)
	}
	return c.gh.createIssueComment(ctx, row.RepositoryFullName, row.PRNumber, body)
}
