"""Exercise the privacy guard against synthetic release mistakes."""

import contextlib
import io
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import check_public_export as guard


class PublicExportTests(unittest.TestCase):
    def scan(self, name, content):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            target = root / name
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text(content)
            with patch.object(guard, "ROOT", root), contextlib.redirect_stdout(io.StringIO()) as output:
                code = guard.check()
            return code, output.getvalue()

    def test_source_examples_are_allowed(self):
        code, _ = self.scan("example.go", 'synthetic@example.invalid /tmp/example generation/home/store/lifecycle')
        self.assertEqual(code, 0)

    def test_home_path_is_blocked_without_echoing_value(self):
        path = "/" + "Users" + "/synthetic-person/work/"
        code, output = self.scan("README.md", path)
        self.assertEqual(code, 1)
        self.assertIn("personal-home-path", output)
        self.assertNotIn(path, output)

    def test_token_is_blocked_without_echoing_value(self):
        token = "ghp_" + "A" * 36
        code, output = self.scan("example.go", token)
        self.assertEqual(code, 1)
        self.assertNotIn(token, output)

    def test_private_directory_is_blocked(self):
        code, output = self.scan(".codexpool/registry.json", "{}")
        self.assertEqual(code, 1)
        self.assertIn("private-or-generated-file", output)

    def test_symlink_is_blocked(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            (root / "README.md").write_text("Synthetic")
            (root / "linked").symlink_to(root / "README.md")
            with patch.object(guard, "ROOT", root), contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(guard.check(), 1)


if __name__ == "__main__":
    unittest.main()
