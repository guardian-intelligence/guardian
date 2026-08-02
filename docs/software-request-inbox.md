# Software request inbox

The company homepage stores each accepted request as a
`company.software_request_submitted` event in `guardian_analytics.events`.
The form reports success only after the ingest service returns one accepted
event and zero rejects.

The request fields live in `props`: `request_id`, `company`, `contact_email`,
`software_kind`, and `request`. The analytics table retains the row for 25
months; the client IP column expires after 90 days.

Use the standard ClickHouse port-forward in `AGENTS.md`, then read the inbox:

```sql
SELECT
    server_ts,
    JSONExtractString(props, 'request_id') AS request_id,
    JSONExtractString(props, 'company') AS company,
    JSONExtractString(props, 'contact_email') AS contact_email,
    JSONExtractString(props, 'software_kind') AS software_kind,
    JSONExtractString(props, 'request') AS request
FROM guardian_analytics.events
WHERE site = 'prod'
  AND event_name = 'company.software_request_submitted'
ORDER BY server_ts DESC;
```

Treat the inbox as confidential customer correspondence. Do not copy request
text or contact details into public issues without the sender's permission.
