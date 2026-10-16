CREATE TABLE jobs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    queue text NOT NULL DEFAULT 'default' CHECK (btrim(queue) <> ''),
    kind text NOT NULL CHECK (kind ~ '^[A-Za-z][A-Za-z0-9_.-]{0,127}$'),
    payload jsonb NOT NULL,
    state text NOT NULL DEFAULT 'available' CHECK (state = 'available'),
    created_at timestamptz NOT NULL DEFAULT now()
);
