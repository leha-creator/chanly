-- +goose Up
-- +goose StatementBegin
CREATE FUNCTION rw_check_settle(p_run uuid,p_nominal bigint,p_fee bigint,p_ledger uuid) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE r public.rw_check_run; g public.rw_check_grant;
BEGIN
    SELECT * INTO STRICT r FROM public.rw_check_run WHERE id=p_run;
    PERFORM 1 FROM public.rw_check_account WHERE id=r.account_id FOR UPDATE;
    SELECT * INTO STRICT r FROM public.rw_check_run WHERE id=p_run FOR UPDATE;
    SELECT * INTO STRICT g FROM public.rw_check_grant WHERE id=r.grant_id;
    IF r.state='settled' THEN
        IF (r.actual_nominal,r.actual_fee,r.ledger_id) IS DISTINCT FROM (p_nominal,p_fee,p_ledger) THEN
            RAISE EXCEPTION 'conflicting diagnostic settlement' USING ERRCODE='23514';
        END IF;
        RETURN;
    END IF;
    IF r.reserved_at IS NULL OR r.state='released' OR p_nominal<0 OR p_fee<0 OR p_ledger IS NULL OR
       NOT EXISTS(SELECT 1 FROM public.ledger_transaction WHERE id=p_ledger AND kind='rw_diagnostic' AND idempotency_key='rw_diagnostic:'||p_run::text) OR
       p_nominal IS DISTINCT FROM (SELECT sum(amount)::bigint FROM public.ledger_entry WHERE ledger_transaction_id=p_ledger AND position=0) OR
       p_fee IS DISTINCT FROM (SELECT coalesce(sum(amount),0)::bigint FROM public.ledger_entry WHERE ledger_transaction_id=p_ledger AND position=1) THEN
        RAISE EXCEPTION 'diagnostic settlement evidence required' USING ERRCODE='23514';
    END IF;
    UPDATE public.rw_check_run SET state='settled',actual_nominal=p_nominal,actual_fee=p_fee,ledger_id=p_ledger,settled_at=clock_timestamp() WHERE id=p_run;
    IF p_nominal<>r.nominal OR p_fee>r.fee_ceiling THEN
        UPDATE public.rw_check_account SET killed=true WHERE id=r.account_id;
    END IF;
    INSERT INTO public.rw_check_audit(actor,operation,grant_id,run_id) VALUES(session_user,'settle',r.grant_id,p_run);
END $$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION rw_check_settle(uuid,bigint,bigint,uuid) FROM PUBLIC;
-- +goose StatementBegin
DO $$ DECLARE runner_role text := nullif(current_setting('maxim.rw_runner_role',true),''); BEGIN
    IF runner_role IS NOT NULL THEN EXECUTE format('GRANT EXECUTE ON FUNCTION rw_check_settle(uuid,bigint,bigint,uuid) TO %I',runner_role); END IF;
END $$;
-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN IF EXISTS(SELECT 1 FROM rw_check_run WHERE state='settled') THEN RAISE EXCEPTION 'diagnostic settlements must be retained'; END IF; END $$;
-- +goose StatementEnd
DROP FUNCTION rw_check_settle(uuid,bigint,bigint,uuid);
