#!/usr/bin/env python3
"""Read-only dev Telegram diagnostics: never print credentials or message payloads."""
import importlib.util
import json
from pathlib import Path
import re
import subprocess


def main():
    root = Path("/opt/maxim-dev")
    spec = importlib.util.spec_from_file_location("configuration", root / "configure-telegram.py")
    configuration = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(configuration)
    file, domain = configuration.target("dev")
    settings = configuration.values(file.read_text())
    webhook = configuration.api(settings["TELEGRAM_BOT_TOKEN"], "getWebhookInfo")
    error = webhook.get("last_error_message", "")
    status = re.search(r"\b[45][0-9]{2}\b", error)
    print(json.dumps({"webhook_matches": webhook.get("url") == "https://" + domain + "/api/v1/webhooks/telegram",
                      "pending_updates": webhook.get("pending_update_count"),
                      "last_error_date": webhook.get("last_error_date"),
                      "last_error_http_status": status.group() if status else None,
                      "last_error_category": ("certificate" if "certificate" in error.lower() else
                                              "timeout" if "timed out" in error.lower() else
                                              "http" if status else "other" if error else None)}), flush=True)
    ids = subprocess.check_output(["docker", "ps", "-q", "--filter", "label=com.docker.compose.project=maxim-dev",
                                   "--filter", "label=com.docker.compose.service=postgres"], text=True).split()
    if len(ids) != 1:
        raise ValueError("Expected exactly one dev database")
    sql = """
BEGIN READ ONLY;
SET LOCAL statement_timeout='5s';
SELECT json_build_object('connections', coalesce(json_agg(t),'[]')) FROM
 (SELECT status, created_at, expires_at, now() >= expires_at AS expired_by_time
  FROM notification_telegram_bot_connection ORDER BY created_at DESC LIMIT 10) t;
SELECT json_build_object('recipients',count(*)) FROM user_notification_telegram_bot;
SELECT json_build_object('inbound', coalesce(json_agg(t),'[]')) FROM
 (SELECT e.status, e.created_at, e.processed_at, e.error IS NOT NULL AS has_error,
  c.status AS connection_status, c.expires_at, e.created_at < c.expires_at AS arrived_before_expiry,
  j.status AS job_status, j.attempts, j.last_error IS NOT NULL AS job_has_error
  FROM inbound_event e LEFT JOIN notification_telegram_bot_connection c
    ON c.token_hash=e.payload->>'token_hash'
  LEFT JOIN job j ON j.dedupe_key='inbound:'||e.id::text
  WHERE e.source='telegram' ORDER BY e.created_at DESC LIMIT 10) t;
COMMIT;
"""
    result = subprocess.run(["docker", "exec", "-i", ids[0], "sh", "-c",
                             'exec psql -X -qAt -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB"'],
                            input=sql, text=True, capture_output=True, check=True)
    print(result.stdout, end="", flush=True)


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        raise SystemExit("Telegram diagnostics failed: " + type(error).__name__) from None
