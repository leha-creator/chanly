#!/usr/bin/env python3
import base64
import importlib.util
import errno
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location(
    "proxy_config", Path(__file__).resolve().parents[1] / "telegram/proxy_config.py")
config = importlib.util.module_from_spec(spec)
spec.loader.exec_module(config)
sys.modules["proxy_config"] = config
installer_spec = importlib.util.spec_from_file_location(
    "telegram_install", Path(__file__).resolve().parents[1] / "telegram/install.py")
installer = importlib.util.module_from_spec(installer_spec)
installer_spec.loader.exec_module(installer)


class TelegramProxyTests(unittest.TestCase):
    def test_full_disk_preserves_existing_private_file(self):
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / "config.json"
            target.write_text("previous working configuration")
            with patch.object(installer.os, "fsync", side_effect=OSError(errno.ENOSPC, "secret URL")):
                with self.assertRaises(OSError):
                    installer.write_private(target, "replacement")
            self.assertEqual(target.read_text(), "previous working configuration")
            self.assertEqual(list(Path(directory).iterdir()), [target])

    def test_private_write_replaces_file(self):
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / "runtime.env"
            installer.write_private(target, "first")
            installer.write_private(target, "second")
            self.assertEqual(target.read_text(), "second")
            if os.name == "posix":
                self.assertEqual(target.stat().st_mode & 0o777, 0o600)

    def test_error_reports_errno_without_credentials(self):
        self.assertEqual(installer.failure_summary(OSError(errno.ENOSPC, "private subscription URL")),
                         "Telegram egress installation failed: OSError errno=28(ENOSPC)")

    def test_healthy_existing_proxy_is_kept_only_for_same_subscription(self):
        rendered = config.render("trojan://test@example.test:443?type=tcp&security=tls")
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "config").mkdir()
            (root / "config/config.json").write_text(json.dumps(rendered))
            (root / "subscription.url").write_text("https://subscription.test/private")
            (root / "runtime.env").write_text("PROXY_UID=1000\nPROXY_GID=1000\n")
            payload = {"subscription_url": "https://subscription.test/private", "config": rendered}
            with patch.object(installer, "ROOT", root), \
                    patch.object(installer.os, "getuid", return_value=1000, create=True), \
                    patch.object(installer.os, "getgid", return_value=1000, create=True), \
                    patch.object(installer, "probe") as probe, patch.object(installer, "run") as run:
                installer.install(payload)
                probe.assert_called_once()
                run.assert_not_called()
                probe.reset_mock()
                payload["subscription_url"] = "https://subscription.test/replaced"
                self.assertFalse(installer.current_is_healthy(payload))
                probe.assert_not_called()
                payload["subscription_url"] = "https://subscription.test/private"
                probe.side_effect = installer.urllib.error.URLError("private details")
                self.assertFalse(installer.current_is_healthy(payload))
                probe.side_effect = None
                rendered["routing"]["rules"][0]["domain"] = ["full:example.com"]
                (root / "config/config.json").write_text(json.dumps(rendered))
                self.assertFalse(installer.current_is_healthy(payload))

    def test_subscription_decoding(self):
        uri = "trojan://test@example.test:443?type=tcp&security=tls"
        self.assertEqual(config.profiles(base64.b64encode(uri.encode()).decode()), [uri])

    def test_default_is_blocked_and_only_bot_api_443_is_tunneled(self):
        rendered = config.render("trojan://test@example.test:443?type=tcp&security=tls")
        self.assertEqual(rendered["outbounds"][0], {"tag": "blocked", "protocol": "blackhole"})
        rules = rendered["routing"]["rules"]
        self.assertEqual(len(rules), 1)
        self.assertEqual(rules[0]["domain"], ["full:api.telegram.org"])
        self.assertEqual(rules[0]["port"], "443")

    def test_vless_and_reality_parameters(self):
        rendered = config.render("vless://00000000-0000-0000-0000-000000000001@example.test:443?type=tcp&security=reality&pbk=test&sid=abcd&fp=chrome&flow=xtls-rprx-vision")
        outbound = rendered["outbounds"][1]
        self.assertEqual(outbound["protocol"], "vless")
        self.assertEqual(outbound["streamSettings"]["realitySettings"]["password"], "test")
        self.assertEqual(outbound["settings"]["vnext"][0]["users"][0]["flow"], "xtls-rprx-vision")

    def test_rejects_insecure_or_unsupported_profiles(self):
        for query in ["security=none", "security=tls&allowInsecure=1", "security=tls&type=unknown"]:
            with self.assertRaises(ValueError):
                config.render("trojan://test@example.test:443?" + query)


if __name__ == "__main__":
    unittest.main()
