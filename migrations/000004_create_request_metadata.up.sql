CREATE TABLE request_metadata (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  provider TEXT NOT NULL,
  model TEXT NOT NULL,
  input_tokens INT NOT NULL DEFAULT 0,
  output_tokens INT NOT NULL DEFAULT 0,
  latency_ms INT NOT NULL DEFAULT 0,
  status TEXT NOT NULL,
  error_type TEXT,
  error_message TEXT,
  cost_usd NUMERIC(12, 8) NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_request_metadata_user_id ON request_metadata(user_id);
CREATE INDEX idx_request_metadata_created_at ON request_metadata(created_at DESC);
CREATE INDEX idx_request_metadata_user_created ON request_metadata(user_id, created_at DESC);
