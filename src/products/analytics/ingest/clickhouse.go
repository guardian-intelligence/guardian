package main

import (
	"context"
	"fmt"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

type clickhouseSink struct {
	conn driver.Conn
}

func newClickHouseSink(addr, database, user, password string) (*clickhouseSink, error) {
	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr: []string{addr},
		Auth: clickhouse.Auth{Database: database, Username: user, Password: password},
		// In-cluster plaintext; the Cilium policy scopes who can reach the
		// port at all. TLS arrives with the OpenBao-managed credentials.
		TLS:         nil,
		DialTimeout: 10 * time.Second,
		Compression: &clickhouse.Compression{Method: clickhouse.CompressionLZ4},
	})
	if err != nil {
		return nil, fmt.Errorf("clickhouse open: %w", err)
	}
	return &clickhouseSink{conn: conn}, nil
}

func (s *clickhouseSink) Ping(ctx context.Context) error {
	return s.conn.Ping(ctx)
}

func (s *clickhouseSink) Insert(ctx context.Context, rows []eventRow) error {
	batch, err := s.conn.PrepareBatch(ctx, `INSERT INTO guardian_analytics.events
		(server_ts, site, event_name, trust_tier, schema_version, trace_id,
		 correlation_id, session_seq, path, referrer, ua, client_ip, ip_source,
		 country, asn, status, duration_ms, client_skew_ms, vital_name,
		 vital_value, props)`)
	if err != nil {
		return fmt.Errorf("prepare batch: %w", err)
	}
	for i := range rows {
		r := &rows[i]
		if err := batch.Append(
			r.ServerTs,
			r.Site,
			r.EventName,
			// clickhouse-go's Enum8 column accepts int/int8/string but NOT
			// uint8 — passing TrustTier raw aborts every insert block.
			int(r.TrustTier),
			r.SchemaVersion,
			string(r.TraceID[:]),
			string(r.CorrelationID[:]),
			r.SessionSeq,
			r.Path,
			r.Referrer,
			r.UA,
			r.ClientIP,
			r.IPSource,
			r.Country,
			r.ASN,
			r.Status,
			r.DurationMs,
			r.ClientSkewMs,
			r.VitalName,
			r.VitalValue,
			r.Props,
		); err != nil {
			batch.Abort()
			return fmt.Errorf("append row %d: %w", i, err)
		}
	}
	return batch.Send()
}
