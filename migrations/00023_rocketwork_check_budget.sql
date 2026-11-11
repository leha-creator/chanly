-- +goose Up
-- Every function uses trusted database time and serializes on the physical account.
-- +goose StatementBegin
CREATE FUNCTION rw_check_intent(p_run uuid,p_action text,p_hash text) RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE r public.rw_check_run; a public.rw_check_account; g public.rw_check_grant;
    previous public.rw_check_action; spent numeric; daily_spent numeric; held numeric;
    operations bigint; at_time timestamptz := clock_timestamp(); day_start timestamptz;
BEGIN
    SELECT * INTO STRICT r FROM public.rw_check_run WHERE id=p_run;
    SELECT * INTO STRICT a FROM public.rw_check_account WHERE id=r.account_id FOR UPDATE;
    SELECT * INTO STRICT r FROM public.rw_check_run WHERE id=p_run;
    SELECT * INTO STRICT g FROM public.rw_check_grant WHERE id=coalesce(r.active_grant_id,r.grant_id) FOR UPDATE;
    SELECT * INTO STRICT r FROM public.rw_check_run WHERE id=p_run FOR UPDATE;
    at_time := clock_timestamp();
    IF a.killed OR g.revoked_at IS NOT NULL OR at_time < g.not_before OR at_time >= g.expires_at OR
       r.state IN ('settled','released') OR NOT coalesce(g.policy->'Actions' ? p_action,false) OR
       g.policy->>'TotalBudget' IS NULL OR g.policy->>'DailyBudget' IS NULL OR g.policy->>'DailyOperations' IS NULL OR
       p_action NOT IN ('create','complete','sign_act','pay','cancel') OR
       r.nominal IS DISTINCT FROM (g.policy->>'FixedAmount')::bigint OR r.fee_ceiling IS DISTINCT FROM (g.policy->>'MaxCommission')::bigint THEN
        RAISE EXCEPTION 'diagnostic action denied' USING ERRCODE='42501';
    END IF;
    SELECT * INTO previous FROM public.rw_check_action WHERE run_id=p_run AND action=p_action;
    IF FOUND THEN
        IF previous.request_hash <> p_hash THEN RAISE EXCEPTION 'action payload conflict' USING ERRCODE='23514'; END IF;
        RETURN false; -- An intent is never a retry permission, even after a crash.
    END IF;
    IF EXISTS(SELECT 1 FROM public.rw_check_action WHERE run_id=p_run AND state IN ('intent','uncertain')) THEN
        RAISE EXCEPTION 'unresolved diagnostic action' USING ERRCODE='42501';
    END IF;
    IF p_action <> 'create' AND (r.task_id IS NULL OR NOT EXISTS(SELECT 1 FROM public.rw_check_action WHERE run_id=p_run AND action='create' AND state='observed')) THEN
        RAISE EXCEPTION 'missing task ownership' USING ERRCODE='42501';
    END IF;
    IF r.state='prepared' THEN
        IF p_action <> 'create' AND r.task_id IS NULL THEN RAISE EXCEPTION 'create required' USING ERRCODE='42501'; END IF;
        day_start := date_trunc('day',at_time AT TIME ZONE 'UTC') AT TIME ZONE 'UTC';
        SELECT coalesce(sum(actual_nominal::numeric+actual_fee),0),
               coalesce(sum(actual_nominal::numeric+actual_fee) FILTER(WHERE settled_at>=day_start),0)
          INTO spent,daily_spent FROM public.rw_check_run WHERE account_id=a.id AND state='settled';
        SELECT coalesce(sum(nominal::numeric+fee_ceiling),0) INTO held FROM public.rw_check_run
          WHERE account_id=a.id AND reserved_at IS NOT NULL AND state NOT IN ('settled','released');
        SELECT count(*) INTO operations FROM public.rw_check_run WHERE account_id=a.id AND reserved_at>=day_start;
        IF spent+held+r.nominal+r.fee_ceiling > least(a.total_limit,(g.policy->>'TotalBudget')::bigint) OR
           daily_spent+held+r.nominal+r.fee_ceiling > least(a.daily_limit,(g.policy->>'DailyBudget')::bigint) OR
           operations >= least(a.daily_operations,(g.policy->>'DailyOperations')::bigint) THEN
            RAISE EXCEPTION 'diagnostic budget exhausted' USING ERRCODE='42501';
        END IF;
        UPDATE public.rw_check_run SET state='reserved',reserved_at=at_time WHERE id=p_run;
    END IF;
    INSERT INTO public.rw_check_action(run_id,action,state,request_hash) VALUES(p_run,p_action,'intent',p_hash);
    INSERT INTO public.rw_check_audit(actor,operation,grant_id,run_id) VALUES(session_user,'intent:'||p_action,g.id,p_run);
    RETURN true;
END $$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION rw_check_observe(p_run uuid,p_action text,p_state text,p_task bigint) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE r public.rw_check_run; old_state text;
BEGIN
    SELECT * INTO STRICT r FROM public.rw_check_run WHERE id=p_run;
    PERFORM 1 FROM public.rw_check_account WHERE id=r.account_id FOR UPDATE;
    SELECT * INTO STRICT r FROM public.rw_check_run WHERE id=p_run FOR UPDATE;
    IF p_state NOT IN ('observed','uncertain','rejected') OR (r.task_id IS NOT NULL AND p_task IS DISTINCT FROM r.task_id) OR
       (p_state='observed' AND p_task IS NULL) THEN RAISE EXCEPTION 'invalid observation' USING ERRCODE='23514'; END IF;
    SELECT state INTO STRICT old_state FROM public.rw_check_action WHERE run_id=p_run AND action=p_action FOR UPDATE;
    IF old_state=p_state THEN RETURN; END IF;
    IF old_state NOT IN ('intent','uncertain') THEN RAISE EXCEPTION 'final observation' USING ERRCODE='23514'; END IF;
    UPDATE public.rw_check_action SET state=p_state,observed_at=clock_timestamp() WHERE run_id=p_run AND action=p_action;
    UPDATE public.rw_check_run SET task_id=coalesce(task_id,p_task),
      state=CASE WHEN p_action='create' AND p_state='rejected' AND task_id IS NULL THEN 'released'
                 WHEN p_state='uncertain' THEN 'uncertain' ELSE 'observed' END WHERE id=p_run;
    INSERT INTO public.rw_check_audit(actor,operation,grant_id,run_id) VALUES(session_user,'observe:'||p_action||':'||p_state,r.grant_id,p_run);
END $$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION rw_check_intent(uuid,text,text),rw_check_observe(uuid,text,text,bigint) FROM PUBLIC;
-- +goose StatementBegin
DO $$ DECLARE runner_role text := nullif(current_setting('maxim.rw_runner_role',true),''); BEGIN
    IF runner_role IS NOT NULL THEN EXECUTE format('GRANT EXECUTE ON FUNCTION rw_check_intent(uuid,text,text),rw_check_observe(uuid,text,text,bigint) TO %I',runner_role); END IF;
END $$;
-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS(SELECT 1 FROM rw_check_action) THEN RAISE EXCEPTION 'diagnostic intents must be retained'; END IF;
END $$;
-- +goose StatementEnd
DROP FUNCTION rw_check_intent(uuid,text,text),rw_check_observe(uuid,text,text,bigint);
