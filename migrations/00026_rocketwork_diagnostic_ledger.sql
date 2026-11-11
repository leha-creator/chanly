-- release: contract
-- The checks are widened transactionally. Arm diagnostic writes only after all
-- old application instances have been retired; old readers do not know this kind.
-- +goose Up
ALTER TABLE ledger_account DROP CONSTRAINT ledger_account_kind_check;
ALTER TABLE ledger_account ADD CONSTRAINT ledger_account_kind_check CHECK(kind IN ('bank','rocketwork_deposit','payout_fees','diagnostic_expense','advertiser_balance','placement_hold','blogger_payable','platform_revenue'));
ALTER TABLE ledger_account DROP CONSTRAINT ledger_account_liability_balance;
ALTER TABLE ledger_account ADD CONSTRAINT ledger_account_liability_balance CHECK(kind IN ('bank','rocketwork_deposit','payout_fees','diagnostic_expense') OR balance>=0);
ALTER TABLE ledger_transaction DROP CONSTRAINT ledger_transaction_kind_check;
ALTER TABLE ledger_transaction ADD CONSTRAINT ledger_transaction_kind_check CHECK(kind IN ('deposit','hold','hold_release','settlement','payout','deposit_topup','balance_return','manual','rw_diagnostic'));

-- Ledger-owned restricted entry point: runner cannot write arbitrary ledger SQL.
-- +goose StatementBegin
CREATE FUNCTION ledger_rw_diagnostic_expense(p_run uuid,p_nominal bigint,p_fee bigint,p_hash text,p_ids uuid[]) RETURNS uuid
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE r public.rw_check_run; existing public.ledger_transaction; dep uuid; expense uuid; fees uuid; at_time timestamptz:=clock_timestamp();
BEGIN
 SELECT * INTO STRICT r FROM public.rw_check_run WHERE id=p_run;
 PERFORM 1 FROM public.rw_check_account WHERE id=r.account_id FOR UPDATE;
 SELECT * INTO STRICT r FROM public.rw_check_run WHERE id=p_run FOR UPDATE;
 IF p_nominal<=0 OR p_fee<0 OR p_fee>9223372036854775807-p_nominal OR r.reserved_at IS NULL OR r.state='released' OR
    r.task_id IS NULL OR NOT EXISTS(SELECT 1 FROM public.rw_check_action WHERE run_id=p_run AND action='pay' AND state='observed') THEN
  RAISE EXCEPTION 'diagnostic expense evidence required' USING ERRCODE='23514';
 END IF;
 SELECT * INTO existing FROM public.ledger_transaction WHERE idempotency_key='rw_diagnostic:'||p_run::text;
 IF FOUND THEN
  IF existing.entries_hash<>p_hash THEN RAISE EXCEPTION 'diagnostic expense conflict' USING ERRCODE='23514'; END IF;
  RETURN existing.id;
 END IF;
 INSERT INTO public.ledger_account(id,kind,balance,created_at,updated_at) VALUES
   (p_ids[1],'rocketwork_deposit',0,at_time,at_time),(p_ids[2],'diagnostic_expense',0,at_time,at_time),(p_ids[3],'payout_fees',0,at_time,at_time)
 ON CONFLICT ON CONSTRAINT ledger_account_key DO NOTHING;
 PERFORM 1 FROM public.ledger_account WHERE kind IN ('rocketwork_deposit','diagnostic_expense','payout_fees') ORDER BY id FOR UPDATE;
 SELECT id INTO STRICT dep FROM public.ledger_account WHERE kind='rocketwork_deposit';
 SELECT id INTO STRICT expense FROM public.ledger_account WHERE kind='diagnostic_expense';
 SELECT id INTO STRICT fees FROM public.ledger_account WHERE kind='payout_fees';
 INSERT INTO public.ledger_transaction(id,kind,actor_type,idempotency_key,entries_hash,created_at)
 VALUES(p_ids[4],'rw_diagnostic','system','rw_diagnostic:'||p_run::text,p_hash,at_time);
 INSERT INTO public.ledger_entry(id,ledger_transaction_id,position,debit_account_id,credit_account_id,amount,created_at)
 VALUES(p_ids[5],p_ids[4],0,expense,dep,p_nominal,at_time);
 IF p_fee>0 THEN
  INSERT INTO public.ledger_entry(id,ledger_transaction_id,position,debit_account_id,credit_account_id,amount,created_at)
  VALUES(p_ids[6],p_ids[4],1,fees,dep,p_fee,at_time);
 END IF;
 UPDATE public.ledger_account SET balance=balance+CASE WHEN id=dep THEN -p_nominal-p_fee WHEN id=expense THEN p_nominal ELSE p_fee END,updated_at=at_time WHERE id IN (dep,expense,fees);
 RETURN p_ids[4];
END $$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION ledger_rw_diagnostic_expense(uuid,bigint,bigint,text,uuid[]) FROM PUBLIC;
-- +goose StatementBegin
DO $$ DECLARE runner_role text:=nullif(current_setting('maxim.rw_runner_role',true),''); BEGIN
 IF runner_role IS NOT NULL THEN EXECUTE format('GRANT EXECUTE ON FUNCTION ledger_rw_diagnostic_expense(uuid,bigint,bigint,text,uuid[]) TO %I',runner_role); END IF;
END $$;
-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN IF EXISTS(SELECT 1 FROM ledger_transaction WHERE kind='rw_diagnostic') THEN RAISE EXCEPTION 'diagnostic ledger history must be retained'; END IF; END $$;
-- +goose StatementEnd
DROP FUNCTION ledger_rw_diagnostic_expense(uuid,bigint,bigint,text,uuid[]);
DELETE FROM ledger_account WHERE kind='diagnostic_expense' AND balance=0;
ALTER TABLE ledger_account DROP CONSTRAINT ledger_account_kind_check;
ALTER TABLE ledger_account ADD CONSTRAINT ledger_account_kind_check CHECK(kind IN ('bank','rocketwork_deposit','payout_fees','advertiser_balance','placement_hold','blogger_payable','platform_revenue'));
ALTER TABLE ledger_account DROP CONSTRAINT ledger_account_liability_balance;
ALTER TABLE ledger_account ADD CONSTRAINT ledger_account_liability_balance CHECK(kind IN ('bank','rocketwork_deposit','payout_fees') OR balance>=0);
ALTER TABLE ledger_transaction DROP CONSTRAINT ledger_transaction_kind_check;
ALTER TABLE ledger_transaction ADD CONSTRAINT ledger_transaction_kind_check CHECK(kind IN ('deposit','hold','hold_release','settlement','payout','deposit_topup','balance_return','manual'));
