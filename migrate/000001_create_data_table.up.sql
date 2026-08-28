CREATE TABLE divisions (
    id   SERIAL PRIMARY KEY,
    name TEXT UNIQUE NOT NULL
);

CREATE TABLE items (
    id   SERIAL PRIMARY KEY,
    name TEXT UNIQUE NOT NULL
);

CREATE TABLE sub_items (
    id      SERIAL PRIMARY KEY,
    name    TEXT UNIQUE NOT NULL,
    item_id INT REFERENCES items(id)
);

CREATE TABLE fin_types (
    id   SERIAL PRIMARY KEY,
    name TEXT UNIQUE NOT NULL
);

CREATE TABLE vids (
    id   SERIAL PRIMARY KEY,
    name TEXT UNIQUE NOT NULL
);

CREATE TABLE data (
    id           BIGSERIAL PRIMARY KEY,
    date         DATE,
    num_oper     TEXT,
    type_oper    TEXT,
    debet_val    NUMERIC,
    credit_val   NUMERIC,
    ex_rate      NUMERIC,
    debet        NUMERIC,
    credit       NUMERIC,
    sender       TEXT,
    description  TEXT,
    bank         TEXT,
    period       DATE,
    organization TEXT,
    division_id  INT REFERENCES divisions(id),
    item_id      INT REFERENCES items(id),
    sub_item_id  INT REFERENCES sub_items(id),
    comment1     TEXT,
    comment2     TEXT,
    fin_type_id  INT REFERENCES fin_types(id),
    sum_dash     NUMERIC,
    vid_id       INT REFERENCES vids(id),
    sum_revenue  NUMERIC,
    sum_cost     NUMERIC,
    sum_return   NUMERIC,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
