#!/usr/bin/env python3
"""Apply only Telegram settings from stdin; never print credentials."""
import argparse
import json
import os
from pathlib import Path
import re
import secrets
import stat
import tempfile
import urllib.error
import urllib.request

TARGETS = {
    "dev": (Path("/opt/maxim-dev"), "dev.chanly.ru"),
    "production": (Path("/opt/maxim-production"), "chanly.ru"),
}
KEYS = {"TELEGRAM_ENABLED", "TELEGRAM_BOT_NAME", "TELEGRAM_BOT_TOKEN",
        "TELEGRAM_WEBHOOK_SECRET", "TELEGRAM_WEBHOOK_URL"}


class ConfigError(ValueError):
    """Only controlled messages may reach the job log."""


def values(source):
    return dict(line.split("=", 1) for line in source.splitlines()
                if "=" in line and not line.lstrip().startswith("#"))


def api(token, method):
    try:
        request = urllib.request.Request(
            "https://api.telegram.org/bot" + token + "/" + method,
            data=b"{}", headers={"Content-Type": "application/json"})
        with urllib.request.urlopen(request, timeout=20) as response:
            result = json.load(response)
        if not result.get("ok"):
            raise ConfigError("telegram_api_refused")
        return result["result"]
    except urllib.error.HTTPError as error:
        raise ConfigError("telegram_api_http_" + str(error.code)) from None
    except urllib.error.URLError as error:
        raise ConfigError("telegram_api_connect_" + type(error.reason).__name__) from None
    except ConfigError:
        raise
    except Exception as error:
        raise ConfigError("telegram_api_" + type(error).__name__) from None


def render(source, token, bot_name, domain):
    if not re.fullmatch(r"[0-9]+:[A-Za-z0-9_-]+", token):
        raise ConfigError("Invalid Telegram token format")
    name = bot_name.removeprefix("@")
    if not re.fullmatch(r"[A-Za-z0-9_]{5,32}", name):
        raise ConfigError("Invalid Telegram username")
    existing = values(source)
    secret = existing.get("TELEGRAM_WEBHOOK_SECRET", "")
    if (existing.get("TELEGRAM_BOT_TOKEN") != token or
            not re.fullmatch(r"[A-Za-z0-9_-]{32,256}", secret)):
        secret = secrets.token_hex(32)
    settings = {
        "TELEGRAM_ENABLED": "true", "TELEGRAM_BOT_NAME": "@" + name,
        "TELEGRAM_BOT_TOKEN": token, "TELEGRAM_WEBHOOK_SECRET": secret,
        "TELEGRAM_WEBHOOK_URL": "https://" + domain + "/api/v1/webhooks/telegram",
    }
    kept = [line for line in source.splitlines()
            if line.split("=", 1)[0] not in KEYS]
    return "\n".join(kept).rstrip("\n") + "\n\n" + "\n".join(
        key + "=" + value for key, value in settings.items()) + "\n"


def target(environment):
    directory, domain = TARGETS[environment]
    file = directory / "app.env"
    if directory.resolve(strict=True) != directory or file.is_symlink():
        raise ConfigError("Unexpected environment path")
    metadata = file.stat()
    if not stat.S_ISREG(metadata.st_mode) or metadata.st_uid != os.getuid():
        raise ConfigError("Environment file must be owned by the current deploy user")
    return file, domain


def configure(environment, payload):
    file, domain = target(environment)
    if set(payload) != {"token", "bot_name"}:
        raise ConfigError("Unexpected Telegram payload")
    token, name = payload["token"], payload["bot_name"]
    updated = render(file.read_text(), token, name, domain)
    if api(token, "getMe").get("username") != name.removeprefix("@"):
        raise ConfigError("Telegram bot identity mismatch")
    fd, temporary = tempfile.mkstemp(prefix=".telegram-env-", dir=file.parent)
    try:
        with os.fdopen(fd, "w") as output:
            os.fchmod(output.fileno(), 0o600)
            output.write(updated)
            output.flush()
            os.fsync(output.fileno())
        os.replace(temporary, file)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)
    print(environment + ": Telegram app.env configured for " + name)


def verify(environment):
    file, domain = target(environment)
    settings = values(file.read_text())
    expected = "https://" + domain + "/api/v1/webhooks/telegram"
    identity = api(settings["TELEGRAM_BOT_TOKEN"], "getMe")
    if (settings.get("TELEGRAM_ENABLED") != "true" or
            identity.get("username") != settings["TELEGRAM_BOT_NAME"].removeprefix("@")):
        raise ConfigError("Telegram bot identity or enabled state mismatch")
    webhook = api(settings["TELEGRAM_BOT_TOKEN"], "getWebhookInfo")
    if webhook.get("url") != expected:
        raise ConfigError("Telegram webhook does not match the selected environment")
    request = urllib.request.Request(expected, data=b'{"update_id":0}', headers={
        "Content-Type": "application/json",
        "X-Telegram-Bot-Api-Secret-Token": settings["TELEGRAM_WEBHOOK_SECRET"],
    })
    try:
        with urllib.request.urlopen(request, timeout=20) as response:
            if response.status != 200:
                raise ValueError()
    except Exception:
        raise ConfigError("Authenticated Telegram webhook probe failed") from None
    print(environment + ": @" + identity["username"] + " webhook verified")


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("environment", choices=TARGETS)
    parser.add_argument("--verify", action="store_true")
    args = parser.parse_args()
    try:
        if args.verify:
            verify(args.environment)
        else:
            import sys
            configure(args.environment, json.load(sys.stdin))
    except ConfigError as error:
        raise SystemExit("Telegram configuration failed: " + str(error)) from None
    except Exception as error:
        raise SystemExit("Telegram configuration failed: " + type(error).__name__) from None
