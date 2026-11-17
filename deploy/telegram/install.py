#!/usr/bin/env python3
"""Install only the fixed, private Telegram egress service. Payload arrives on stdin."""
import errno
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

from proxy_config import IMAGE, PORT

ROOT = Path("/opt/maxim-dev/telegram-egress")
NETWORK = "chanly-telegram-egress"


def run(*args, check=True):
    return subprocess.run(args, check=check, capture_output=True, text=True)


def write_private(path, value):
    # A full disk must not truncate the last working configuration or credentials.
    fd, temporary = tempfile.mkstemp(prefix="." + path.name + ".", dir=path.parent)
    try:
        with os.fdopen(fd, "w") as file:
            file.write(value)
            file.flush()
            os.fsync(file.fileno())
        os.replace(temporary, path)
    finally:
        Path(temporary).unlink(missing_ok=True)


def probe():
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({"https": "http://127.0.0.1:18081"}))
    try:
        opener.open("https://api.telegram.org/bot0:invalid/getMe", timeout=12)
    except urllib.error.HTTPError as error:
        if error.code != 401:
            raise ValueError("Telegram probe did not reach the expected API") from None
    else:
        raise ValueError("Unexpected Telegram probe response")
    try:
        with opener.open("https://example.com", timeout=4):
            raise ValueError("Non-Telegram destination was not rejected")
    except (urllib.error.URLError, TimeoutError, ConnectionError):
        pass


def validate_config(config):
    rules = config["routing"]["rules"]
    if (config["outbounds"][0] != {"tag": "blocked", "protocol": "blackhole"} or
            len(rules) != 1 or rules[0]["domain"] != ["full:api.telegram.org"] or
            rules[0]["port"] != "443" or len(config["inbounds"]) != 1 or
            config["inbounds"][0]["port"] != PORT):
        raise ValueError("Telegram-only routing is required")


def current_is_healthy(payload):
    try:
        if (ROOT / "subscription.url").read_text().strip() != payload["subscription_url"].strip():
            return False
        validate_config(json.loads((ROOT / "config/config.json").read_text()))
        expected = "PROXY_UID=" + str(os.getuid()) + "\nPROXY_GID=" + str(os.getgid()) + "\n"
        if (ROOT / "runtime.env").read_text() != expected:
            return False
        probe()
        return True
    except (OSError, ValueError, KeyError, TypeError, IndexError):
        return False


def failure_summary(error):
    # Exception messages/URLs and subprocess stderr can contain subscription secrets.
    reason = error.reason if isinstance(error, urllib.error.URLError) else error
    code = getattr(reason, "errno", None)
    suffix = ""
    if isinstance(code, int):
        suffix = " errno=" + str(code) + "(" + errno.errorcode.get(code, "UNKNOWN") + ")"
    return "Telegram egress installation failed: " + type(error).__name__ + suffix


def install(payload):
    if os.getuid() == 0 or ROOT.resolve(strict=True) != ROOT:
        raise ValueError("Expected canonical directory owned by deploy")
    config = payload["config"]
    validate_config(config)
    if current_is_healthy(payload):
        print("Existing Telegram egress verified: same subscription, Telegram API reachable, other destinations rejected.")
        return
    directory = ROOT / "config"
    directory.mkdir(mode=0o700, exist_ok=True)
    candidate = directory / "candidate.json"
    write_private(candidate, json.dumps(config, sort_keys=True))
    run("docker", "pull", IMAGE)
    run("docker", "run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL",
        "--user", str(os.getuid()) + ":" + str(os.getgid()),
        "--mount", "type=bind,src=" + str(directory) + ",dst=/etc/xray,readonly",
        IMAGE, "run", "-test", "-config", "/etc/xray/candidate.json")
    existing = run("docker", "network", "inspect", NETWORK, check=False)
    if existing.returncode:
        run("docker", "network", "create", "--label", "chanly.telegram=true", NETWORK)
    elif json.loads(existing.stdout)[0].get("Labels", {}).get("chanly.telegram") != "true":
        raise ValueError("Unexpected network owner")
    current = directory / "config.json"
    old = current.read_text() if current.exists() else None
    changed = old != candidate.read_text()
    write_private(ROOT / "subscription.url", payload["subscription_url"])
    write_private(ROOT / "runtime.env", "PROXY_UID=" + str(os.getuid()) + "\nPROXY_GID=" + str(os.getgid()) + "\n")
    os.replace(candidate, current)
    command = ["docker", "compose", "--env-file", str(ROOT / "runtime.env"), "-f", str(ROOT / "compose.yml")]
    try:
        run(*command, "up", "-d", *(["--force-recreate"] if changed else []))
        for attempt in range(5):
            try:
                probe()
                break
            except Exception:
                if attempt == 4:
                    raise
                time.sleep(2)
    except Exception:
        if old is not None:
            write_private(current, old)
            run(*command, "up", "-d", "--force-recreate", check=False)
        else:
            run(*command, "down", check=False)
        raise
    print("Xray installed: Telegram API reachable; other destinations rejected; host port is loopback-only.")
    cron = run("crontab", "-l", check=False).stdout
    lines = [line for line in cron.splitlines() if "# chanly-telegram-egress" not in line]
    lines.append("17 */6 * * * /usr/bin/flock -n " + str(ROOT / "refresh.lock") +
                 " /usr/bin/python3 " + str(ROOT / "install.py") + " --refresh >" +
                 str(ROOT / "refresh.log") + " 2>&1 # chanly-telegram-egress")
    subprocess.run(["crontab", "-"], input="\n".join(lines) + "\n", text=True, check=True, capture_output=True)


if __name__ == "__main__":
    try:
        if sys.argv[1:] == ["--refresh"]:
            from proxy_config import fetch, profiles, render
            url = (ROOT / "subscription.url").read_text().strip()
            payload = {"subscription_url": url, "config": render(profiles(fetch(url))[0])}
        else:
            payload = json.load(sys.stdin)
        install(payload)
    except Exception as error:
        raise SystemExit(failure_summary(error)) from None
