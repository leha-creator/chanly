-- Run explicitly on the trusted control DB as its administrator, with psql -v
-- runner_password=... -v policy_password=... through protected operator access.
-- Do not add either role to the app role, backup-reader role, or each other.
SELECT format('CREATE ROLE maxim_rw_runner LOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE PASSWORD %L', :'runner_password') \gexec
SELECT format('CREATE ROLE maxim_rw_policy LOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE PASSWORD %L', :'policy_password') \gexec
-- Then migrate with DB_ROCKETWORK_RUNNER_ROLE=maxim_rw_runner and
-- DB_ROCKETWORK_POLICY_ROLE=maxim_rw_policy. Network access is ops-host only.
