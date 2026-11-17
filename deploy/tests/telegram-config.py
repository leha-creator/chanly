#!/usr/bin/env python3
"""Regression checks for the isolated Telegram environment editor."""
import importlib.util
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location(
    "telegram_config", Path(__file__).resolve().parents[1] / "app/configure-telegram.py")
config = importlib.util.module_from_spec(spec)
spec.loader.exec_module(config)


class TelegramConfigTests(unittest.TestCase):
    def test_preserves_other_settings_and_removes_duplicate_telegram_keys(self):
        source = "DATABASE_URL=unchanged\n# Comment\nTELEGRAM_ENABLED=false\nTELEGRAM_ENABLED=true\n"
        updated = config.render(source, "123:test", "@ExampleBot", "dev.chanly.ru")
        self.assertIn("DATABASE_URL=unchanged\n# Comment\n", updated)
        self.assertEqual(updated.count("TELEGRAM_ENABLED="), 1)
        self.assertIn("TELEGRAM_ENABLED=true", updated)
        self.assertEqual(config.render(updated, "123:test", "@ExampleBot", "dev.chanly.ru"), updated)

    def test_rejects_environment_injection(self):
        for token, name in [("123:test\nOTHER=bad", "BotName"), ("123:test", "Bot\nOTHER=bad")]:
            with self.assertRaises(ValueError):
                config.render("", token, name, "dev.chanly.ru")

    def test_identity_mismatch_does_not_write(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            file = root / "app.env"
            file.write_text("UNCHANGED=true\n")
            with patch.dict(config.TARGETS, {"dev": (root, "dev.chanly.ru")}), patch.object(
                    config, "api", return_value={"username": "DifferentBot"}):
                with self.assertRaises(ValueError):
                    config.configure("dev", {"token": "123:test", "bot_name": "ExampleBot"})
            self.assertEqual(file.read_text(), "UNCHANGED=true\n")

    def test_atomic_write_is_private_and_keeps_other_fields(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            file = root / "app.env"
            file.write_text("UNCHANGED=true\n")
            with patch.dict(config.TARGETS, {"dev": (root, "dev.chanly.ru")}), patch.object(
                    config, "api", return_value={"username": "ExampleBot"}):
                config.configure("dev", {"token": "123:test", "bot_name": "ExampleBot"})
            self.assertEqual(file.stat().st_mode & 0o777, 0o600)
            self.assertIn("UNCHANGED=true", file.read_text())


if __name__ == "__main__":
    unittest.main()
