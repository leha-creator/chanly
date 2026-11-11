-- +goose Up
-- The policy owner may prepare its own immutable approved run without a token.
-- This grants no provider write, intent, settlement or ledger capability.
-- +goose StatementBegin
DO $$ DECLARE policy_role text:=nullif(current_setting('maxim.rw_policy_role',true),''); BEGIN
 IF policy_role IS NOT NULL THEN
  EXECUTE format('GRANT EXECUTE ON FUNCTION rw_check_prepare(uuid,uuid,text,jsonb,text,bigint,bigint) TO %I',policy_role);
 END IF;
END $$;
-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin
DO $$ DECLARE policy_role text:=nullif(current_setting('maxim.rw_policy_role',true),''); BEGIN
 IF policy_role IS NOT NULL THEN
  EXECUTE format('REVOKE EXECUTE ON FUNCTION rw_check_prepare(uuid,uuid,text,jsonb,text,bigint,bigint) FROM %I',policy_role);
 END IF;
END $$;
-- +goose StatementEnd
