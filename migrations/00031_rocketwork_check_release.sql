-- +goose Up
-- Only a proven unsigned cancellation with zero fee can release a live task's
-- unused hold. Rejected creates are handled atomically by rw_check_observe.
-- +goose StatementBegin
CREATE FUNCTION rw_check_release(p_run uuid) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE r public.rw_check_run;
BEGIN
 SELECT * INTO STRICT r FROM public.rw_check_run WHERE id=p_run;
 PERFORM 1 FROM public.rw_check_account WHERE id=r.account_id FOR UPDATE;
 SELECT * INTO STRICT r FROM public.rw_check_run WHERE id=p_run FOR UPDATE;
 IF r.state='released' THEN RETURN; END IF;
 IF r.state='settled' OR r.reserved_at IS NULL OR
    EXISTS(SELECT 1 FROM public.rw_check_action WHERE run_id=p_run AND action='pay') OR
    NOT EXISTS(SELECT 1 FROM public.rw_check_action WHERE run_id=p_run AND action='cancel' AND state='observed') THEN
  RAISE EXCEPTION 'cancellation evidence required' USING ERRCODE='23514';
 END IF;
 UPDATE public.rw_check_run SET state='released' WHERE id=p_run;
 INSERT INTO public.rw_check_audit(actor,operation,run_id) VALUES(session_user,'release_proven_zero_charge',p_run);
END $$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION rw_check_release(uuid) FROM PUBLIC;
-- +goose StatementBegin
DO $$ DECLARE runner_role text:=nullif(current_setting('maxim.rw_runner_role',true),''); BEGIN
 IF runner_role IS NOT NULL THEN EXECUTE format('GRANT EXECUTE ON FUNCTION rw_check_release(uuid) TO %I',runner_role); END IF;
END $$;
-- +goose StatementEnd
-- +goose Down
DROP FUNCTION rw_check_release(uuid);
