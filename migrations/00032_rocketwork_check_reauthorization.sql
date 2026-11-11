-- +goose Up
-- A restore/expiry admission can change authority, never immutable run terms or
-- committed actions. Only the policy owner can bind the independently issued grant.
-- +goose StatementBegin
CREATE FUNCTION rw_check_reauthorize(p_run uuid,p_grant uuid) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE r public.rw_check_run; g public.rw_check_grant; at_time timestamptz;
BEGIN
 SELECT * INTO STRICT r FROM public.rw_check_run WHERE id=p_run;
 PERFORM 1 FROM public.rw_check_account WHERE id=r.account_id FOR UPDATE;
 SELECT * INTO STRICT g FROM public.rw_check_grant WHERE id=p_grant FOR UPDATE;
 SELECT * INTO STRICT r FROM public.rw_check_run WHERE id=p_run FOR UPDATE;
 at_time:=clock_timestamp();
 IF r.state IN ('settled','released') OR g.account_id<>r.account_id OR g.revoked_at IS NOT NULL OR
    at_time<g.not_before OR at_time>=g.expires_at OR g.policy->>'RunID' IS DISTINCT FROM p_run::text OR
    g.policy->>'ManifestHash' IS DISTINCT FROM r.manifest_hash OR coalesce(g.policy->>'ArmingID','')='' OR
    (g.policy->>'FixedAmount')::bigint IS DISTINCT FROM r.nominal OR (g.policy->>'MaxCommission')::bigint IS DISTINCT FROM r.fee_ceiling THEN
  RAISE EXCEPTION 'diagnostic reauthorization denied' USING ERRCODE='42501';
 END IF;
 UPDATE public.rw_check_run SET active_grant_id=p_grant WHERE id=p_run;
 INSERT INTO public.rw_check_audit(actor,operation,grant_id,run_id) VALUES(session_user,'reauthorize',p_grant,p_run);
END $$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION rw_check_reauthorize(uuid,uuid) FROM PUBLIC;
-- +goose StatementBegin
DO $$ DECLARE policy_role text:=nullif(current_setting('maxim.rw_policy_role',true),''); BEGIN
 IF policy_role IS NOT NULL THEN EXECUTE format('GRANT EXECUTE ON FUNCTION rw_check_reauthorize(uuid,uuid) TO %I',policy_role); END IF;
END $$;
-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN IF EXISTS(SELECT 1 FROM rw_check_run WHERE active_grant_id IS NOT NULL) THEN RAISE EXCEPTION 'reauthorization history must be retained'; END IF; END $$;
-- +goose StatementEnd
DROP FUNCTION rw_check_reauthorize(uuid,uuid);
