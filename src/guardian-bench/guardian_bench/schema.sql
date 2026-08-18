PRAGMA foreign_keys = ON;

CREATE TABLE places (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('home', 'restaurant', 'office', 'park', 'shop')),
    seating TEXT NOT NULL CHECK (seating IN ('indoor', 'outdoor', 'mixed', 'n/a'))
);

CREATE TABLE person (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    home_place_id INTEGER REFERENCES places (id)
);

CREATE TABLE contacts (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    reliability TEXT NOT NULL CHECK (reliability IN ('reliable', 'flaky')),
    phone TEXT
);

CREATE TABLE events (
    id INTEGER PRIMARY KEY,
    title TEXT NOT NULL,
    contact_id INTEGER REFERENCES contacts (id),
    place_id INTEGER REFERENCES places (id),
    starts_at TEXT NOT NULL,
    ends_at TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('booked', 'tentative', 'cancelled'))
);

CREATE TABLE items (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    location TEXT
);

CREATE TABLE reminders (
    id INTEGER PRIMARY KEY,
    event_id INTEGER NOT NULL REFERENCES events (id),
    target TEXT NOT NULL CHECK (target IN ('principal', 'contact')),
    contact_id INTEGER REFERENCES contacts (id),
    remind_at TEXT NOT NULL,
    note TEXT NOT NULL
);

CREATE TABLE event_prep (
    id INTEGER PRIMARY KEY,
    event_id INTEGER NOT NULL REFERENCES events (id),
    item_id INTEGER REFERENCES items (id),
    note TEXT
);

CREATE TABLE assistant_questions (
    id INTEGER PRIMARY KEY,
    event_id INTEGER REFERENCES events (id),
    topic TEXT NOT NULL,
    question TEXT NOT NULL
);

CREATE TABLE forecast (
    hour_start TEXT NOT NULL,
    precip_prob INTEGER NOT NULL CHECK (precip_prob BETWEEN 0 AND 100),
    temp_c INTEGER NOT NULL,
    issued_at TEXT NOT NULL
);

CREATE TABLE venue_hours (
    place_id INTEGER NOT NULL REFERENCES places (id),
    dow INTEGER NOT NULL CHECK (dow BETWEEN 1 AND 7),
    opens TEXT NOT NULL,
    closes TEXT NOT NULL
);

CREATE TABLE transit (
    from_place_id INTEGER NOT NULL REFERENCES places (id),
    to_place_id INTEGER NOT NULL REFERENCES places (id),
    mode TEXT NOT NULL CHECK (mode IN ('walk', 'drive', 'transit', 'bike')),
    minutes INTEGER NOT NULL
);
