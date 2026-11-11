-- +goose Up
-- Runner may stop new writes on a discrepancy, but can never re-arm an account.
-- +goose StatementBegin
CREATE FUNCTION rw_check_freeze(p_run uuid) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE account text;
BEGIN
 SELECT account_id INTO STRICT account FROM public.rw_check_run WHERE id=p_run;
 UPDATE public.rw_check_account SET killed=true WHERE id=account;
 INSERT INTO public.rw_check_audit(actor,operation,run_id) VALUES(session_user,'financial_discrepancy_freeze',p_run);
END $$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION rw_check_freeze(uuid) FROM PUBLIC;
-- +goose StatementBegin
DO $$ DECLARE runner_role text:=nullif(current_setting('maxim.rw_runner_role',true),''); BEGIN
 IF runner_role IS NOT NULL THEN EXECUTE format('GRANT EXECUTE ON FUNCTION rw_check_freeze(uuid) TO %I',runner_role); END IF;
END $$;
-- +goose StatementEnd
-- +goose Down
DROP FUNCTION rw_check_freeze(uuid);
