-- +goose Up
-- Roles are provisioned independently with LOGIN credentials on the ops host.
-- Migration does not create passwords, memberships or access from dev networks.
-- +goose StatementBegin
DO $$ DECLARE app_role text:=current_setting('maxim.app_role'); policy_role text:=nullif(current_setting('maxim.rw_policy_role',true),''); runner_role text:=nullif(current_setting('maxim.rw_runner_role',true),''); BEGIN
 IF policy_role=app_role OR runner_role=app_role OR policy_role=runner_role THEN RAISE EXCEPTION 'control roles must be distinct'; END IF;
 IF runner_role IS NOT NULL THEN
  EXECUTE format('GRANT USAGE ON SCHEMA public TO %I',runner_role);
  EXECUTE format('GRANT SELECT ON rw_check_account,rw_check_alias,rw_check_grant,rw_check_run,rw_check_action,rw_check_legacy_import,rw_check_dispatch_ownership TO %I',runner_role);
  EXECUTE format('GRANT SELECT,INSERT,UPDATE ON rate_limit_bucket,outbound_circuit TO %I',runner_role);
  EXECUTE format('GRANT INSERT ON outbound_request TO %I',runner_role);
  EXECUTE format('GRANT EXECUTE ON FUNCTION rw_check_prepare(uuid,uuid,text,jsonb,text,bigint,bigint),rw_check_intent(uuid,text,text),rw_check_observe(uuid,text,text,bigint),rw_check_settle(uuid,bigint,bigint,uuid),rw_check_import(uuid,text,text,bigint,text,text,text),ledger_rw_diagnostic_expense(uuid,bigint,bigint,text,uuid[]) TO %I',runner_role);
 END IF;
 IF policy_role IS NOT NULL THEN
  EXECUTE format('GRANT USAGE ON SCHEMA public TO %I',policy_role);
  EXECUTE format('GRANT SELECT,INSERT,UPDATE ON rw_check_account,rw_check_alias,rw_check_grant TO %I',policy_role);
  EXECUTE format('GRANT SELECT ON rw_check_run,rw_check_action,rw_check_audit TO %I',policy_role);
  EXECUTE format('GRANT EXECUTE ON FUNCTION rw_check_issue(uuid,jsonb),rw_check_revoke(uuid),rw_check_kill(text,boolean) TO %I',policy_role);
 END IF;
 EXECUTE format('REVOKE ALL ON rw_check_ownership,rw_check_dispatch_ownership FROM %I',app_role);
 EXECUTE format('GRANT SELECT ON rw_check_ownership,rw_check_dispatch_ownership TO %I',app_role);
END $$;
-- +goose StatementEnd
-- +goose Down
-- Permission reduction only: history is owned by earlier migrations.
-- +goose StatementBegin
DO $$ DECLARE runner_role text:=nullif(current_setting('maxim.rw_runner_role',true),''); BEGIN
 IF runner_role IS NOT NULL THEN
  EXECUTE format('REVOKE SELECT,INSERT,UPDATE ON rate_limit_bucket,outbound_circuit FROM %I',runner_role);
  EXECUTE format('REVOKE INSERT ON outbound_request FROM %I',runner_role);
 END IF;
END $$;
-- +goose StatementEnd
