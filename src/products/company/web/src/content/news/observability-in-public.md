---
slug: observability-in-public
kicker: Note
category: note
title: Observability, in public.
deck: Every route on this site emits a trace that lands in our own ClickHouse, on the same pipeline our customers use. We publish what we see there.
date: 5 April 2026
publishedAt: 2026-04-05
authorName: Shovon Hasan
authorRole: Founder & CEO
---

The site runs on the same telemetry surface as the rest of the platform. Every route mount, every card click, every subscribe submit is a span. The spans land in ClickHouse. The same evidence we ask of our own services we ask of our own website.

The point is not that we have telemetry. The point is that when we say a feature shipped and works, a queryable artifact says so.
