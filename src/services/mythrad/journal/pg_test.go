package journal_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/guardian-intelligence/guardian/src/services/mythrad/journal"
	"github.com/guardian-intelligence/guardian/src/services/mythrad/journal/journaltest"
	"github.com/guardian-intelligence/guardian/src/services/postflight/controlplane/pgtest"
)

func TestPgConformance(t *testing.T) {
	journaltest.Run(t, func(t *testing.T) journal.Journal {
		pool, err := pgxpool.New(context.Background(), pgtest.Start(t))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		j := journal.NewPg(pool)
		if err := j.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
		return j
	})
}
