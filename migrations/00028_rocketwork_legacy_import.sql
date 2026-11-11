-- +goose Up
CREATE TABLE rw_check_legacy_import (
 id uuid PRIMARY KEY,
 account_id text NOT NULL REFERENCES rw_check_account(id),
 external_id text NOT NULL,
 task_id bigint NOT NULL CHECK(task_id>0),
 manifest_hash text NOT NULL CHECK(manifest_hash ~ '^[a-f0-9]{64}$'),
 terms_hash text NOT NULL CHECK(terms_hash ~ '^[a-f0-9]{64}$'),
 observed_status text NOT NULL,
 observed_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(account_id,external_id),UNIQUE(account_id,task_id)
);
-- +goose StatementBegin
CREATE FUNCTION rw_check_import(p_id uuid,p_account text,p_external text,p_task bigint,p_hash text,p_terms text,p_status text) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
BEGIN
 IF p_external<>'integration-check:'||p_id::text THEN RAISE EXCEPTION 'invalid legacy diagnostic identity'; END IF;
 INSERT INTO public.rw_check_legacy_import(id,account_id,external_id,task_id,manifest_hash,terms_hash,observed_status)
 VALUES(p_id,p_account,p_external,p_task,p_hash,p_terms,p_status) ON CONFLICT(id) DO NOTHING;
 IF NOT EXISTS(SELECT 1 FROM public.rw_check_legacy_import WHERE id=p_id AND account_id=p_account AND external_id=p_external AND task_id=p_task AND manifest_hash=p_hash AND terms_hash=p_terms) THEN
  RAISE EXCEPTION 'legacy import conflict' USING ERRCODE='23514';
 END IF;
 INSERT INTO public.rw_check_audit(actor,operation,run_id) VALUES(session_user,'legacy_readonly_import',p_id);
END $$;
-- +goose StatementEnd
REVOKE ALL ON rw_check_legacy_import FROM PUBLIC;
REVOKE ALL ON FUNCTION rw_check_import(uuid,text,text,bigint,text,text,text) FROM PUBLIC;
-- +goose StatementBegin
DO $$ DECLARE app_role text:=current_setting('maxim.app_role'); runner_role text:=nullif(current_setting('maxim.rw_runner_role',true),''); BEGIN
 EXECUTE format('REVOKE ALL ON rw_check_legacy_import FROM %I',app_role);
 IF runner_role IS NOT NULL THEN
  EXECUTE format('GRANT SELECT ON rw_check_legacy_import TO %I',runner_role);
  EXECUTE format('GRANT EXECUTE ON FUNCTION rw_check_import(uuid,text,text,bigint,text,text,text) TO %I',runner_role);
 END IF;
END $$;
-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN IF EXISTS(SELECT 1 FROM rw_check_legacy_import) THEN RAISE EXCEPTION 'legacy import history must be retained'; END IF; END $$;
-- +goose StatementEnd
DROP FUNCTION rw_check_import(uuid,text,text,bigint,text,text,text);
DROP TABLE rw_check_legacy_import;
