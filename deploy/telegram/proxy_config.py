#!/usr/bin/env python3
"""Render an allowlisted Xray proxy from a private VLESS/Trojan subscription."""
import base64
import json
import os
import re
import sys
import urllib.parse
import urllib.request
import uuid

IMAGE = "ghcr.io/xtls/xray-core@sha256:9812e382e229e9ed63fd179bf1a4e3fe244c12ff3ae4fd8128745d79c87575ab"
PORT = 18081


def profiles(body):
    text = body.strip()
    if not text.startswith(("vless://", "trojan://")):
        text = base64.b64decode(text + "=" * (-len(text) % 4), validate=True).decode()
    return [line.strip() for line in text.splitlines()
            if line.strip().startswith(("vless://", "trojan://"))]


def render(uri):
    node = urllib.parse.urlsplit(uri)
    params = dict(urllib.parse.parse_qsl(node.query))
    if node.scheme not in {"vless", "trojan"} or not node.hostname or not node.port:
        raise ValueError("Unsupported subscription profile")
    if params.get("type", "tcp") not in {"tcp", "raw"}:
        raise ValueError("Only RAW/TCP subscription transports are supported")
    security = params.get("security", "tls")
    if security not in {"tls", "reality"} or params.get("allowInsecure", "0").lower() in {"1", "true"}:
        raise ValueError("Authenticated transport security is required")
    credential = urllib.parse.unquote(node.username or "")
    if not credential:
        raise ValueError("Subscription authentication is required")
    if node.scheme == "vless":
        uuid.UUID(credential)
        settings = {"vnext": [{"address": node.hostname, "port": node.port,
                    "users": [{"id": credential, "encryption": "none", "flow": params.get("flow", "")}]}]}
    else:
        settings = {"servers": [{"address": node.hostname, "port": node.port, "password": credential}]}
    stream = {"method": "raw", "security": security}
    server_name = params.get("sni", node.hostname)
    if security == "reality":
        if not params.get("pbk") or not re.fullmatch(r"(?:[0-9a-fA-F]{2}){0,8}", params.get("sid", "")):
            raise ValueError("Invalid REALITY parameters")
        stream["realitySettings"] = {
            "serverName": server_name, "fingerprint": params.get("fp", "chrome"),
            "password": params["pbk"], "shortId": params.get("sid", ""),
            "spiderX": params.get("spx", "/"), "show": False,
        }
    else:
        stream["tlsSettings"] = {"serverName": server_name, "allowInsecure": False,
                                 "fingerprint": params.get("fp", "chrome")}
    return {
        "log": {"loglevel": "none", "access": "none", "error": "none"},
        "inbounds": [{"tag": "telegram-http", "listen": "0.0.0.0", "port": PORT,
                      "protocol": "http", "settings": {"allowTransparent": False}}],
        # Default outbound always rejects. Only the exact Telegram API host:443 is tunneled.
        "outbounds": [{"tag": "blocked", "protocol": "blackhole"},
                      {"tag": "subscription", "protocol": node.scheme,
                       "settings": settings, "streamSettings": stream}],
        "routing": {"domainStrategy": "AsIs", "rules": [
            {"type": "field", "inboundTag": ["telegram-http"], "network": "tcp",
             "domain": ["full:api.telegram.org"], "port": "443", "outboundTag": "subscription"},
        ]},
    }


def fetch(url):
    if urllib.parse.urlsplit(url).scheme != "https":
        raise ValueError("HTTPS subscription URL required")
    request = urllib.request.Request(url, headers={"User-Agent": "v2rayN/7.15"})
    with urllib.request.urlopen(request, timeout=30) as response:
        body = response.read(2 * 1024 * 1024 + 1)
    if len(body) > 2 * 1024 * 1024:
        raise ValueError("Subscription too large")
    return body.decode()


if __name__ == "__main__":
    try:
        if sys.argv[1:] == ["--render"]:
            body = sys.stdin.read()
            json.dump(render(profiles(body)[0]), sys.stdout)
        else:
            url = os.environ["TELEGRAM_VLESS_SUBSCRIPTION_URL"].strip()
            candidates = profiles(fetch(url))
            if not candidates:
                raise ValueError("No supported subscription profiles")
            json.dump({"subscription_url": url, "config": render(candidates[0])}, sys.stdout)
    except Exception as error:
        raise SystemExit("Subscription preparation failed: " + type(error).__name__) from None
