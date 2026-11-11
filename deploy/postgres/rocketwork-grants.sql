-- Apply after migrations, also when 00029 was applied without control-role names.
-- psql -v app_role=maxim_app -f rocketwork-grants.sql
-- Run as the trusted DB administrator. Never down/reset control tables to grant access.
\set ON_ERROR_STOP on
BEGIN;
SELECT pg_advisory_xact_lock(729405114);
SELECT set_config('maxim.setup_app_role', :'app_role', true);
DO $$
DECLARE app_role text:=current_setting('maxim.setup_app_role');
BEGIN
 IF app_role IN ('maxim_rw_runner','maxim_rw_policy') OR
    NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname=app_role) THEN
  RAISE EXCEPTION 'existing distinct app role required';
 END IF;
 IF (SELECT count(*) FROM pg_roles WHERE rolname IN ('maxim_rw_runner','maxim_rw_policy')
    AND rolcanlogin AND NOT (rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls OR rolinherit)) <> 2
    OR EXISTS(SELECT 1 FROM pg_auth_members WHERE roleid IN
       (SELECT oid FROM pg_roles WHERE rolname IN ('maxim_rw_runner','maxim_rw_policy'))
       OR member IN (SELECT oid FROM pg_roles WHERE rolname IN ('maxim_rw_runner','maxim_rw_policy'))) THEN
  RAISE EXCEPTION 'isolated control roles required';
 END IF;
 -- No DDL, truncation, counter reset or policy arming occurs here.
 GRANT USAGE ON SCHEMA public TO maxim_rw_runner,maxim_rw_policy;
 GRANT SELECT ON rw_check_account,rw_check_alias,rw_check_grant,rw_check_run,rw_check_action,rw_check_legacy_import,rw_check_dispatch_ownership TO maxim_rw_runner;
 GRANT SELECT,INSERT,UPDATE ON rate_limit_bucket,outbound_circuit TO maxim_rw_runner;
 GRANT INSERT ON outbound_request TO maxim_rw_runner;
 GRANT EXECUTE ON FUNCTION rw_check_prepare(uuid,uuid,text,jsonb,text,bigint,bigint),rw_check_intent(uuid,text,text),rw_check_observe(uuid,text,text,bigint),rw_check_settle(uuid,bigint,bigint,uuid),rw_check_import(uuid,text,text,bigint,text,text,text),ledger_rw_diagnostic_expense(uuid,bigint,bigint,text,uuid[]),rw_check_freeze(uuid),rw_check_release(uuid),rw_check_paid_at(uuid,timestamptz) TO maxim_rw_runner;
 GRANT SELECT,INSERT,UPDATE ON rw_check_account,rw_check_alias,rw_check_grant TO maxim_rw_policy;
 GRANT SELECT ON rw_check_run,rw_check_action,rw_check_audit TO maxim_rw_policy;
 GRANT EXECUTE ON FUNCTION rw_check_issue(uuid,jsonb),rw_check_revoke(uuid),rw_check_kill(text,boolean),rw_check_reauthorize(uuid,uuid) TO maxim_rw_policy;
 GRANT EXECUTE ON FUNCTION rw_check_prepare(uuid,uuid,text,jsonb,text,bigint,bigint) TO maxim_rw_policy;
 EXECUTE format('REVOKE ALL ON rw_check_ownership,rw_check_dispatch_ownership FROM %I',app_role);
 EXECUTE format('GRANT SELECT ON rw_check_ownership,rw_check_dispatch_ownership TO %I',app_role);
END $$;
COMMIT;
