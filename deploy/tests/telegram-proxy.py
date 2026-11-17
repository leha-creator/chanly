#!/usr/bin/env python3
import base64
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location(
    "proxy_config", Path(__file__).resolve().parents[1] / "telegram/proxy_config.py")
config = importlib.util.module_from_spec(spec)
spec.loader.exec_module(config)


class TelegramProxyTests(unittest.TestCase):
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
