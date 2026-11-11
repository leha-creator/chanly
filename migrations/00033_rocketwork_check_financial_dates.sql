-- +goose Up
-- Provider financial time is observed evidence, not a clock for quota authority.
-- +goose StatementBegin
CREATE FUNCTION rw_check_paid_at(p_run uuid,p_at timestamptz) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
BEGIN
 IF p_at IS NULL THEN RAISE EXCEPTION 'provider time required'; END IF;
 UPDATE public.rw_check_run SET provider_paid_at=p_at WHERE id=p_run AND task_id IS NOT NULL AND reserved_at IS NOT NULL;
 IF NOT FOUND THEN RAISE EXCEPTION 'owned reserved task required'; END IF;
END $$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION rw_check_paid_at(uuid,timestamptz) FROM PUBLIC;
-- +goose StatementBegin
DO $$ DECLARE runner_role text:=nullif(current_setting('maxim.rw_runner_role',true),''); BEGIN
 IF runner_role IS NOT NULL THEN EXECUTE format('GRANT EXECUTE ON FUNCTION rw_check_paid_at(uuid,timestamptz) TO %I',runner_role); END IF;
END $$;
-- +goose StatementEnd
-- +goose Down
DROP FUNCTION rw_check_paid_at(uuid,timestamptz);
