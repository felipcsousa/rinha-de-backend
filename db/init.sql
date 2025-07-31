CREATE TABLE payments (
    id SERIAL PRIMARY KEY,
    correlation_id UUID NOT NULL,
    amount NUMERIC(10, 2) NOT NULL,
    processor VARCHAR(10) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_payments_processor_created_at ON payments(processor, created_at);

-- Create a unique index to prevent duplicate correlation_id entries
CREATE UNIQUE INDEX idx_payments_correlation_id ON payments(correlation_id);
