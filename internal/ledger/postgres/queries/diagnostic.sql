-- name: RecordDiagnosticExpense :one
SELECT ledger_rw_diagnostic_expense(sqlc.arg(run_id)::uuid,sqlc.arg(nominal)::bigint,sqlc.arg(fee)::bigint,sqlc.arg(entries_hash)::text,sqlc.arg(ids)::uuid[]);
