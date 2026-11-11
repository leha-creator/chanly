-- release: contract
-- Outbound checks are widened transactionally; existing system values stay valid.
-- Enable TBank writes only after retiring old application instances, whose readers
-- do not know the new system source and nullable file metadata.
-- +goose Up
ALTER TABLE outbound_request DROP CONSTRAINT outbound_request_system_check;
ALTER TABLE outbound_request ADD CONSTRAINT outbound_request_system_check CHECK (system IN ('max','yandex_ord','rocketwork','tbank'));
ALTER TABLE outbound_circuit DROP CONSTRAINT outbound_circuit_system_check;
ALTER TABLE outbound_circuit ADD CONSTRAINT outbound_circuit_system_check CHECK (system IN ('max','yandex_ord','rocketwork','tbank'));
ALTER TABLE bank_statement ALTER COLUMN uploaded_by DROP NOT NULL;
ALTER TABLE bank_statement ALTER COLUMN storage_key DROP NOT NULL;
ALTER TABLE bank_statement ALTER COLUMN file_sha256 DROP NOT NULL;
ALTER TABLE bank_statement ADD COLUMN source text NOT NULL DEFAULT 'file' CHECK (source IN ('file','tbank'));
ALTER TABLE bank_statement ADD COLUMN complete boolean NOT NULL DEFAULT true;
ALTER TABLE bank_statement ADD CONSTRAINT bank_statement_origin_check CHECK (
 (source='file' AND uploaded_by IS NOT NULL AND storage_key IS NOT NULL AND file_sha256 IS NOT NULL) OR
 (source='tbank' AND uploaded_by IS NULL AND storage_key IS NULL AND file_sha256 IS NULL));
CREATE TABLE bank_sync_progress (
 account_number text PRIMARY KEY,
 progress jsonb NOT NULL,
 updated_at timestamptz NOT NULL DEFAULT now()
);
-- Identity aliases are shared by API and file imports, including the legacy 1C hash.
CREATE TABLE bank_operation_identity (
 account_number text NOT NULL,
 alias text NOT NULL,
 external_id text NOT NULL,
 fingerprint text NOT NULL,
 PRIMARY KEY(account_number,alias)
);
CREATE TABLE bank_operation_observation (
 id uuid PRIMARY KEY,
 account_number text NOT NULL,
 operation_id text NOT NULL,
 external_id text NOT NULL,
 document_number text NOT NULL,
 doc_date timestamptz,
 charge_date timestamptz,
 operation_date timestamptz NOT NULL,
 conflict boolean NOT NULL,
    statement_id uuid NOT NULL REFERENCES bank_statement(id),
    evidence jsonb NOT NULL,
 observed_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX bank_operation_observation_account_idx ON bank_operation_observation(account_number, observed_at);
CREATE INDEX bank_operation_observation_period_idx ON bank_operation_observation(account_number, operation_date, external_id);
CREATE INDEX bank_operation_observation_seen_idx ON bank_operation_observation(statement_id, account_number, external_id);
-- +goose StatementBegin
DO $$ DECLARE app_role text := current_setting('maxim.app_role'); BEGIN
 EXECUTE format('REVOKE UPDATE, DELETE, TRUNCATE ON bank_operation_identity, bank_operation_observation FROM %I', app_role);
END $$;
-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM bank_statement WHERE source='tbank') OR EXISTS (SELECT 1 FROM bank_operation_observation) THEN
 RAISE EXCEPTION 'bank import history must be retained'; END IF;
END $$;
-- +goose StatementEnd
DROP TABLE bank_operation_observation;
DROP TABLE bank_operation_identity;
DROP TABLE bank_sync_progress;
ALTER TABLE bank_statement DROP CONSTRAINT bank_statement_origin_check;
ALTER TABLE bank_statement DROP COLUMN source, DROP COLUMN complete;
ALTER TABLE bank_statement ALTER COLUMN uploaded_by SET NOT NULL;
ALTER TABLE bank_statement ALTER COLUMN storage_key SET NOT NULL;
ALTER TABLE bank_statement ALTER COLUMN file_sha256 SET NOT NULL;
ALTER TABLE outbound_request DROP CONSTRAINT outbound_request_system_check;
ALTER TABLE outbound_request ADD CONSTRAINT outbound_request_system_check CHECK (system IN ('max','yandex_ord','rocketwork'));
ALTER TABLE outbound_circuit DROP CONSTRAINT outbound_circuit_system_check;
ALTER TABLE outbound_circuit ADD CONSTRAINT outbound_circuit_system_check CHECK (system IN ('max','yandex_ord','rocketwork'));
