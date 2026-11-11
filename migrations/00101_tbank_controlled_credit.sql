-- +goose Up
CREATE TABLE bank_credit_policy (
 run_id uuid PRIMARY KEY,
 policy_sha256 text NOT NULL,
 account_number text NOT NULL,
 organization_id uuid NOT NULL REFERENCES organization(id),
 max_payment bigint NOT NULL CHECK (max_payment>0),
 max_total bigint NOT NULL CHECK (max_total>=max_payment),
 expires_at timestamptz NOT NULL,
 spent bigint NOT NULL DEFAULT 0 CHECK (spent>=0 AND spent<=max_total)
);
CREATE TABLE bank_credit_spend (
 bank_transaction_id uuid PRIMARY KEY REFERENCES bank_transaction(id),
 run_id uuid NOT NULL REFERENCES bank_credit_policy(run_id),
 amount bigint NOT NULL CHECK(amount>0),
 created_at timestamptz NOT NULL
);
-- +goose StatementBegin
CREATE FUNCTION guard_bank_credit_policy() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF (NEW.run_id,NEW.policy_sha256,NEW.account_number,NEW.organization_id,NEW.max_payment,NEW.max_total,NEW.expires_at)
 IS DISTINCT FROM (OLD.run_id,OLD.policy_sha256,OLD.account_number,OLD.organization_id,OLD.max_payment,OLD.max_total,OLD.expires_at)
 OR NEW.spent<OLD.spent THEN RAISE EXCEPTION 'immutable bank credit admission' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER bank_credit_policy_guard BEFORE UPDATE ON bank_credit_policy FOR EACH ROW EXECUTE FUNCTION guard_bank_credit_policy();
-- +goose StatementBegin
DO $$ DECLARE app_role text:=current_setting('maxim.app_role'); BEGIN
 EXECUTE format('REVOKE DELETE, TRUNCATE ON bank_credit_policy FROM %I',app_role);
 EXECUTE format('REVOKE UPDATE, DELETE, TRUNCATE ON bank_credit_spend FROM %I',app_role);
END $$;
-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM bank_credit_policy) THEN RAISE EXCEPTION 'bank credit admission history must be retained'; END IF;
END $$;
-- +goose StatementEnd
DROP TABLE bank_credit_spend;
DROP TABLE bank_credit_policy;
DROP FUNCTION guard_bank_credit_policy();
