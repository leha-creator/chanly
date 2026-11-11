-- +goose Up
CREATE VIEW rw_check_dispatch_ownership AS
 SELECT a.alias,r.account_id,r.external_id,r.task_id,r.id AS run_id
 FROM rw_check_run r JOIN rw_check_alias a ON a.account_id=r.account_id;
-- +goose StatementBegin
DO $$ DECLARE app_role text:=current_setting('maxim.app_role'); BEGIN
 EXECUTE format('GRANT SELECT ON rw_check_dispatch_ownership TO %I',app_role);
END $$;
-- +goose StatementEnd
-- +goose Down
DROP VIEW rw_check_dispatch_ownership;
