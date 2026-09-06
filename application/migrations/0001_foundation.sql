-- Authentication schemas belong to the future Better Auth integration.
-- Never edit applied migrations; add the next numbered file instead.
CREATE TABLE factory_application.foundation (
  id boolean PRIMARY KEY DEFAULT true CHECK (id),
  ready_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO factory_application.foundation (id) VALUES (true);
