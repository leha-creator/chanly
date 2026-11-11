-- Run explicitly on the trusted control DB as its administrator, with psql -v
-- runner_password=... -v policy_password=... through protected operator access.
-- Do not add either role to the app role, backup-reader role, or each other.
\set ON_ERROR_STOP on
SELECT length(:'runner_password') >= 32 AND length(:'policy_password') >= 32 AS passwords_valid \gset
\if :passwords_valid
\else
\quit 1
\endif
-- A repeated setup must not rotate credentials used by an existing runner.
-- Reject existing elevated roles rather than silently making them look safe.
BEGIN;
SELECT pg_advisory_xact_lock(729405114);
DO $$ BEGIN
 IF EXISTS (
  SELECT 1 FROM pg_roles WHERE rolname IN ('maxim_rw_runner','maxim_rw_policy')
   AND (rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls OR rolinherit OR NOT rolcanlogin)
 ) OR EXISTS (
  SELECT 1 FROM pg_auth_members m JOIN pg_roles r ON r.oid=m.roleid
   JOIN pg_roles u ON u.oid=m.member
  WHERE r.rolname IN ('maxim_rw_runner','maxim_rw_policy') OR u.rolname IN ('maxim_rw_runner','maxim_rw_policy')
 ) THEN RAISE EXCEPTION 'existing RocketWork roles have unsafe privileges or memberships'; END IF;
END $$;
SELECT format('CREATE ROLE maxim_rw_runner LOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD %L', :'runner_password')
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='maxim_rw_runner') \gexec
SELECT format('CREATE ROLE maxim_rw_policy LOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD %L', :'policy_password')
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='maxim_rw_policy') \gexec
COMMIT;
-- Then migrate with DB_ROCKETWORK_RUNNER_ROLE=maxim_rw_runner and
-- DB_ROCKETWORK_POLICY_ROLE=maxim_rw_policy. Network access is ops-host only.
