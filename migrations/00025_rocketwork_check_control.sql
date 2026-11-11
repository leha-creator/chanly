-- +goose Up
-- +goose StatementBegin
CREATE FUNCTION rw_check_issue(p_id uuid,p_policy jsonb) RETURNS uuid
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE a public.rw_check_account;
BEGIN
 SELECT * INTO STRICT a FROM public.rw_check_account WHERE id=p_policy->>'AccountID' FOR UPDATE;
 IF a.company_inn IS DISTINCT FROM p_policy->>'CompanyINN' OR coalesce(p_policy->>'ArmingID','')='' THEN
  RAISE EXCEPTION 'diagnostic account/arming mismatch' USING ERRCODE='42501';
 END IF;
 INSERT INTO public.rw_check_grant(id,account_id,version,purpose,issued_by,policy,not_before,expires_at)
 VALUES(p_id,a.id,(p_policy->>'Version')::bigint,'diagnostic',session_user,p_policy,(p_policy->>'NotBefore')::timestamptz,(p_policy->>'ExpiresAt')::timestamptz);
 INSERT INTO public.rw_check_audit(actor,operation,grant_id) VALUES(session_user,'issue',p_id);
 RETURN p_id;
END $$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE FUNCTION rw_check_revoke(p_id uuid) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
BEGIN
 UPDATE public.rw_check_grant SET revoked_at=clock_timestamp() WHERE id=p_id AND revoked_at IS NULL;
 IF NOT FOUND THEN RETURN; END IF;
 INSERT INTO public.rw_check_audit(actor,operation,grant_id) VALUES(session_user,'revoke',p_id);
END $$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE FUNCTION rw_check_kill(p_account text,p_killed boolean) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
BEGIN
 UPDATE public.rw_check_account SET killed=p_killed WHERE id=p_account;
 IF NOT FOUND THEN RAISE EXCEPTION 'unknown diagnostic account'; END IF;
 INSERT INTO public.rw_check_audit(actor,operation) VALUES(session_user,CASE WHEN p_killed THEN 'kill' ELSE 'arm' END);
END $$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION rw_check_issue(uuid,jsonb),rw_check_revoke(uuid),rw_check_kill(text,boolean) FROM PUBLIC;
-- +goose StatementBegin
DO $$ DECLARE policy_role text := nullif(current_setting('maxim.rw_policy_role',true),''); BEGIN
 IF policy_role IS NOT NULL THEN EXECUTE format('GRANT EXECUTE ON FUNCTION rw_check_issue(uuid,jsonb),rw_check_revoke(uuid),rw_check_kill(text,boolean) TO %I',policy_role); END IF;
END $$;
-- +goose StatementEnd
-- +goose Down
DROP FUNCTION rw_check_issue(uuid,jsonb),rw_check_revoke(uuid),rw_check_kill(text,boolean);
