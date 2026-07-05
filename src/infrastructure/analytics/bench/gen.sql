CREATE DATABASE IF NOT EXISTS lab;

-- Wire-truth source: types as they arrive at the ingest boundary (strings on
-- the wire stay strings). Insertion order preserved (ORDER BY tuple()) so
-- variant inserts see realistic near-monotonic arrival.
-- Identity attributes (ip, ua, geo, asn) are VISITOR-stable with small churn,
-- like real traffic; drawing them per-event would understate the compression
-- available to visitor-clustered ORDER BY layouts.
DROP TABLE IF EXISTS lab.events_source;
CREATE TABLE lab.events_source
(
    server_ts      DateTime64(3),
    site           String,
    event_name     String,
    trust_tier     UInt8,
    schema_version UInt8,
    trace_id       FixedString(16),
    span_id        FixedString(8),
    correlation_id UUID,
    session_seq    UInt16,
    path           String,
    referrer       String,
    ua             String,
    client_ip      String,
    ip_source      String,
    country        String,
    asn            UInt32,
    status         UInt16,
    duration_ms    UInt32,
    client_ts      DateTime64(3),
    vital_name     String,
    vital_value    Float64,
    props          String
)
ENGINE = MergeTree ORDER BY tuple();

INSERT INTO lab.events_source
WITH
    ['prod','prod','prod','prod','prod','prod','beta','gamma'] AS sites,
    ['page_view','page_view','page_view','page_view','web_vital','web_vital','web_vital','rpc','click','scroll_depth','outbound_link','error'] AS enames,
    ['/','/letters/dear-shovon','/letters','/news','/about','/careers','/letters/first-light','/letters/harbor','/news/launch','/news/dark-bundle','/contact','/privacy','/terms','/news/series-a','/letters/afterword','/products','/products/runners','/products/iam','/docs','/docs/quickstart','/docs/api','/blog','/blog/why-guardian','/status','/security','/press','/team','/investors','/faq','/pricing','/changelog','/legal','/letters/anniversary','/news/hiring','/docs/sdk','/docs/cli','/products/analytics','/blog/postmortem','/blog/design','/news/partnership','/careers/sre','/careers/design','/careers/founding','/docs/self-host','/products/dark','/letters/index','/news/index','/og/dear-shovon','/sitemap.xml','/account','/account/organization','/account/billing','/account/keys','/login','/signup','/onboarding','/dashboard','/settings','/api-explorer','/support'] AS paths,
    ['','','','','','','https://news.ycombinator.com/','https://www.google.com/','https://t.co/x','https://www.linkedin.com/','https://duckduckgo.com/','https://www.bing.com/','https://old.reddit.com/r/selfhosted/','https://lobste.rs/','https://github.com/guardian-intelligence/'] AS refs,
    ['Windows NT 10.0; Win64; x64','Macintosh; Intel Mac OS X 10_15_7','X11; Linux x86_64','iPhone; CPU iPhone OS 17_5 like Mac OS X','Macintosh; Apple M3 Mac OS X 14_5','Linux; Android 14; Pixel 8'] AS oses,
    ['US','US','US','DE','GB','CA','FR','IN','NL','JP','AU','BR','SE','PL','KR','SG','CH','ES','IT','MX','ID','TR','NO','FI','DK','IE','AT','BE','CZ','PT','RO','HU','GR','IL','AE','SA','TH','VN','PH','MY','NZ','ZA','AR','CL','CO','UA','RS','BG','HR','EE'] AS countries,
    ['LCP','INP','CLS','TTFB','FCP'] AS vitals
SELECT
    server_ts, site, event_name, trust_tier, schema_version, trace_id, span_id,
    correlation_id, session_seq, path, referrer,
    concat('Mozilla/5.0 (', oses[1 + toUInt32(pow(cityHash64(vid, 1) / 18446744073709551616., 1.8) * 6)],
           ') AppleWebKit/537.36 (KHTML, like Gecko) Chrome/1',
           toString(20 + cityHash64(vid, 2) % 18), '.0.0.0 Safari/537.36')    AS ua,
    if(cityHash64(vid, 3) % 100 < 6,
       concat(['2a02:4f8:','2a01:cb00:','2600:1700:','2001:8003:'][1 + toUInt32(cityHash64(vid, 4) % 4)],
              substring(lower(hex(sipHash64(vid, ip_epoch))), 1, 4), '::',
              substring(lower(hex(sipHash64(vid, ip_epoch, 1))), 1, 4)),
       IPv4NumToString(bitOr(ip_base, cityHash64(vid, ip_epoch, 2) % 256)))   AS client_ip,
    if(trust_tier = 2, 'cloudflare', if(trust_tier = 1, 'internal', ''))      AS ip_source,
    countries[1 + toUInt32(pow((cityHash64(ip_base) % 1000) / 1000., 2.0) * 50)] AS country,
    toUInt32(3200 + (cityHash64(ip_base) % 900) * 13)                         AS asn,
    status, duration_ms, client_ts, vital_name,
    multiIf(vital_name = 'CLS',  (rand(21) % 400) / 1000.0,
            vital_name = 'LCP',  800 + pow(rand(21) % 1000, 1.4),
            vital_name = 'INP',  16 + (rand(21) % 480),
            vital_name = 'TTFB', 60 + (rand(21) % 900),
            vital_name = 'FCP',  400 + (rand(21) % 2200),
            0.0)                                                              AS vital_value,
    multiIf(event_name = 'scroll_depth', concat('{"depth_pct":', toString(rand(22) % 101), '}'),
            event_name = 'click',        concat('{"target":"', ['cta','nav','footer','card','share'][1 + rand(22) % 5], '"}'),
            event_name = 'error',        '{"kind":"unhandledrejection"}',
            '')                                                               AS props
FROM
(
    SELECT
        number,
        toDateTime64('2026-06-04 00:00:00', 3)
            + (number / 8.0) + (rand(1) % 997) / 1000.0                       AS server_ts,
        sites[1 + rand(2) % 8]                                                AS site,
        enames[1 + toUInt32(pow(rand(3) / 4294967296., 1.7) * 12)]            AS event_name,
        multiIf(rand(4) % 100 < 78, 2, rand(4) % 100 < 92, 3, 1)              AS trust_tier,
        1                                                                     AS schema_version,
        randomFixedString(16)                                                 AS trace_id,
        randomFixedString(8)                                                  AS span_id,
        toUInt32(pow(rand(5) / 4294967296., 2.0) * 400000)                    AS vid,
        reinterpretAsUUID(sipHash128(toString(vid)))                          AS correlation_id,
        toUInt16(pow(rand(6) / 4294967296., 3.0) * 60)                        AS session_seq,
        paths[1 + toUInt32(pow(rand(7) / 4294967296., 2.2) * 60)]             AS path,
        refs[1 + toUInt32(pow(rand(8) / 4294967296., 1.6) * 15)]              AS referrer,
        -- ~4% of a visitor's traffic arrives from a second network (mobile/wifi churn)
        if(rand(9) % 100 < 4, 1, 0)                                           AS ip_epoch,
        bitAnd(toUInt32(pow(cityHash64(vid, 5, ip_epoch) / 18446744073709551616., 1.4)
            * 3200000000 + 167772160), 4294967040)                            AS ip_base,
        multiIf(rand(17) % 1000 < 962, 200, rand(17) % 1000 < 975, 304,
                rand(17) % 1000 < 985, 301, rand(17) % 1000 < 996, 404, 500)  AS status,
        toUInt32(least(exp(3.4 + (rand(18) % 1000) / 240.0), 60000))          AS duration_ms,
        if(trust_tier = 3, server_ts - (rand(19) % 2800) / 1000.0,
           toDateTime64(0, 3))                                                AS client_ts,
        if(event_name = 'web_vital', vitals[1 + rand(20) % 5], '')            AS vital_name
    FROM numbers(20000000)
)
SETTINGS max_insert_threads = 8, max_block_size = 1048576;
