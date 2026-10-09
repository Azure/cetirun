# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Contracts for the built-in Agent Identity Azure skill and its PVC sync."""

import hashlib
import os
import subprocess
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
CHART = ROOT / "app" / "cetirun-chart"
SKILL = CHART / "skills" / "azure" / "SKILL.md"
MANIFEST_NAME = ".cetirun-managed-skill"
SYNC_SCRIPT = CHART / "configs" / "builtin-skill-sync.sh"


class AgentIDRuntimeAssetTests(unittest.TestCase):
    def test_azure_skill_is_typed_read_only_and_handles_auth_states(self):
        content = SKILL.read_text(encoding="utf-8")
        self.assertIn("name: azure-readonly", content)
        for command in (
            "cetirun-agent auth status",
            "cetirun-agent auth start",
            "cetirun-agent azure subscriptions list",
            "cetirun-agent azure resource-groups list",
            "cetirun-agent azure resources search",
            "cetirun-agent azure resource show",
        ):
            with self.subTest(command=command):
                self.assertIn(command, content)
        for prohibited in (
            "`az`",
            "Azure PowerShell",
            "direct ARM",
            "raw KQL",
            "access token",
            "write, delete, deployment, policy",
        ):
            with self.subTest(prohibited=prohibited):
                self.assertIn(prohibited, content)
        for error_code in (
            "authorization_required",
            "interaction_required",
            "forbidden",
            "throttled",
            "dependency_unavailable",
            "invalid_request",
        ):
            with self.subTest(error_code=error_code):
                self.assertIn(error_code, content)
        self.assertIn("untrusted data", content)
        self.assertIn("`actionUrl`", content)

    def test_both_runtime_images_package_the_same_skill(self):
        expected_destinations = {
            "agents/openclaw/Dockerfile": "/home/vibe/.openclaw/skills/azure/",
            "agents/hermes/Dockerfile": "/home/vibe/.hermes/skills/azure/",
        }
        for dockerfile, destination in expected_destinations.items():
            with self.subTest(dockerfile=dockerfile):
                content = (CHART / dockerfile).read_text(encoding="utf-8")
                self.assertIn(
                    f"COPY --chown=vibe:vibe skills/azure/ {destination}", content
                )
                self.assertIn(
                    "COPY --chown=vibe:vibe configs/builtin-skill-sync.sh "
                    "/usr/local/share/cetirun/builtin-skill-sync.sh",
                    content,
                )
        self.assertTrue((SKILL.parent / MANIFEST_NAME).is_file())

    def test_managed_manifest_matches_skill_payload(self):
        manifest = self._manifest(SKILL.parent)
        self.assertEqual(manifest["format"], "1")
        self.assertEqual(manifest["skill"], "azure-readonly")
        self.assertTrue(manifest["version"].isdigit())
        self.assertEqual(manifest["content_sha256"], self._payload_checksum(SKILL.parent))
        self.assertNotIn("awk", SYNC_SCRIPT.read_text(encoding="utf-8"))

    def test_pvc_initializers_use_the_shared_managed_skill_synchronizer(self):
        openclaw = (
            CHART / "agents" / "openclaw" / "bootstrap" / "init-home.sh"
        ).read_text(encoding="utf-8")
        self.assertIn('"$SOURCE_HOME/.openclaw/skills"', openclaw)
        self.assertIn('"$(home_path .openclaw/skills)"', openclaw)
        self.assertIn("sync_managed_image_skills", openclaw)
        self.assertIn("cetirun_sync_builtin_skills", openclaw)
        self.assertIn("builtin-skill-sync.sh", openclaw)

        hermes = (
            CHART / "agents" / "hermes" / "bootstrap" / "hermes-init-home.sh"
        ).read_text(encoding="utf-8")
        self.assertIn('"$SOURCE_HOME/.hermes/skills"', hermes)
        self.assertIn('"$(home_path .hermes/skills)"', hermes)
        self.assertIn("cetirun_sync_builtin_skills", hermes)
        self.assertIn("builtin-skill-sync.sh", hermes)

    def test_chart_templates_mount_runtime_specific_initializer_scripts(self):
        configmap = (CHART / "templates" / "configmap.yaml").read_text(
            encoding="utf-8"
        )
        self.assertIn('.Files.Get "configs/builtin-skill-sync.sh"', configmap)

        openclaw = (
            CHART / "templates" / "agents" / "openclaw" / "_runtime.tpl"
        ).read_text(encoding="utf-8")
        self.assertIn('command: ["/bin/sh", "/etc/cetirun/init-home.sh"]', openclaw)
        self.assertIn("value: /etc/cetirun/builtin-skill-sync.sh", openclaw)

        hermes = (
            CHART / "templates" / "agents" / "hermes" / "_runtime.tpl"
        ).read_text(encoding="utf-8")
        self.assertIn('command: ["/bin/sh", "/etc/cetirun/hermes-init-home.sh"]', hermes)
        self.assertIn("value: /etc/cetirun/builtin-skill-sync.sh", hermes)
        self.assertIn(
            '.Files.Get "agents/hermes/bootstrap/hermes-init-home.sh"',
            hermes,
        )
        self.assertNotIn('.Files.Get "configs/builtin-skill-sync.sh"', hermes)
        self.assertNotIn('.Files.Get "configs/builtin-skill-sync.sh"', openclaw)

    def test_sync_installs_to_empty_pvcs_for_both_runtimes(self):
        for runtime in ("openclaw", "hermes"):
            with self.subTest(runtime=runtime), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                source = root / "image" / f".{runtime}" / "skills"
                destination = root / "pvc" / f".{runtime}" / "skills"
                self._write_managed_skill(source, "new managed payload\n", version="2")

                result = self._sync(source, destination)

                self.assertEqual(result.returncode, 0, result.stderr)
                installed = destination / "azure"
                self.assertEqual((installed / "SKILL.md").read_text(), "new managed payload\n")
                self.assertEqual(self._manifest(installed)["version"], "2")
                self.assertIn("Installed managed skill: azure", result.stdout)

    def test_sync_upgrades_an_unmodified_old_managed_skill_for_both_runtimes(self):
        for runtime in ("openclaw", "hermes"):
            with self.subTest(runtime=runtime), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                source = root / "image" / f".{runtime}" / "skills"
                destination = root / "pvc" / f".{runtime}" / "skills"
                old_payload = "old managed payload\n"
                self._write_managed_skill(
                    source,
                    "new managed payload\n",
                    version="2",
                    previous_payloads=(old_payload,),
                )
                self._write_managed_skill(destination, old_payload, version="1")

                result = self._sync(source, destination)

                self.assertEqual(result.returncode, 0, result.stderr)
                upgraded = destination / "azure"
                self.assertEqual((upgraded / "SKILL.md").read_text(), "new managed payload\n")
                self.assertEqual(self._manifest(upgraded)["version"], "2")
                self.assertIn("Upgraded managed skill: azure", result.stdout)

    def test_sync_adopts_the_known_pre_manifest_release_for_both_runtimes(self):
        for runtime in ("openclaw", "hermes"):
            with self.subTest(runtime=runtime), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                source = root / "image" / f".{runtime}" / "skills"
                destination = root / "pvc" / f".{runtime}" / "skills"
                old_payload = "old managed payload\n"
                self._write_managed_skill(
                    source,
                    "new managed payload\n",
                    version="2",
                    previous_payloads=(old_payload,),
                )
                legacy_skill = destination / "azure"
                legacy_skill.mkdir(parents=True)
                (legacy_skill / "SKILL.md").write_text(old_payload, encoding="utf-8")

                result = self._sync(source, destination)

                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(
                    (legacy_skill / "SKILL.md").read_text(), "new managed payload\n"
                )
                self.assertIn("Adopting the recognized legacy managed skill", result.stdout)

    def test_sync_preserves_a_user_owned_same_name_skill_for_both_runtimes(self):
        for runtime in ("openclaw", "hermes"):
            with self.subTest(runtime=runtime), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                source = root / "image" / f".{runtime}" / "skills"
                destination = root / "pvc" / f".{runtime}" / "skills"
                self._write_managed_skill(source, "new managed payload\n", version="2")
                user_skill = destination / "azure"
                user_skill.mkdir(parents=True)
                (user_skill / "SKILL.md").write_text("user customized payload\n")

                result = self._sync(source, destination)

                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(
                    (user_skill / "SKILL.md").read_text(), "user customized payload\n"
                )
                self.assertIn("preserving conflicting azure", result.stderr)

    def test_sync_preserves_a_user_skill_with_a_forged_manifest_for_both_runtimes(self):
        for runtime in ("openclaw", "hermes"):
            with self.subTest(runtime=runtime), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                source = root / "image" / f".{runtime}" / "skills"
                destination = root / "pvc" / f".{runtime}" / "skills"
                self._write_managed_skill(source, "new managed payload\n", version="2")
                self._write_managed_skill(destination, "user customized payload\n", version="1")

                result = self._sync(source, destination)

                self.assertEqual(result.returncode, 0, result.stderr)
                user_skill = destination / "azure"
                self.assertEqual(
                    (user_skill / "SKILL.md").read_text(), "user customized payload\n"
                )
                self.assertIn("not an image-recognized managed revision", result.stderr)

    def test_sync_preserves_managed_skill_with_newline_in_user_filename(self):
        for runtime in ("openclaw", "hermes"):
            with self.subTest(runtime=runtime), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                source = root / "image" / f".{runtime}" / "skills"
                destination = root / "pvc" / f".{runtime}" / "skills"
                old_payload = "old managed payload\n"
                self._write_managed_skill(
                    source,
                    "new managed payload\n",
                    version="2",
                    previous_payloads=(old_payload,),
                )
                self._write_managed_skill(destination, old_payload, version="1")
                user_file = destination / "azure" / "notes\n.md"
                user_file.write_text("user content\n", encoding="utf-8")

                result = self._sync(source, destination)

                self.assertEqual(result.returncode, 0, result.stderr)
                preserved = destination / "azure"
                self.assertEqual((preserved / "SKILL.md").read_text(), old_payload)
                self.assertEqual(user_file.read_text(), "user content\n")
                self.assertIn("preserving customized managed skill", result.stderr)

    @staticmethod
    def _manifest(skill_dir):
        values = {}
        for line in (skill_dir / MANIFEST_NAME).read_text(encoding="utf-8").splitlines():
            key, value = line.split("=", 1)
            values[key] = value
        return values

    @staticmethod
    def _payload_checksum(skill_dir):
        entries = []
        for path in sorted(skill_dir.rglob("*"), key=lambda value: value.as_posix()):
            if path.name == MANIFEST_NAME:
                continue
            relative = f"./{path.relative_to(skill_dir).as_posix()}"
            if path.is_dir():
                entries.append(f"d {relative}\n")
            elif path.is_file():
                digest = hashlib.sha256(path.read_bytes()).hexdigest()
                entries.append(f"f {relative} {digest}\n")
            else:
                raise AssertionError(f"unexpected skill entry: {path}")
        return hashlib.sha256("".join(entries).encode("utf-8")).hexdigest()

    def _write_managed_skill(self, skills_dir, payload, version, previous_payloads=()):
        skill = skills_dir / "azure"
        skill.mkdir(parents=True)
        (skill / "SKILL.md").write_text(payload, encoding="utf-8")
        manifest_lines = [
            "format=1",
            "skill=azure-readonly",
            f"version={version}",
            f"content_sha256={self._payload_checksum(skill)}",
        ]
        for previous_payload in previous_payloads:
            digest = hashlib.sha256(previous_payload.encode("utf-8")).hexdigest()
            payload_checksum = hashlib.sha256(
                f"f ./SKILL.md {digest}\n".encode("utf-8")
            ).hexdigest()
            manifest_lines.append(f"previous_content_sha256={payload_checksum}")
        (skill / MANIFEST_NAME).write_text(
            "\n".join((*manifest_lines, "")),
            encoding="utf-8",
        )

    @staticmethod
    def _sync(source, destination):
        environment = os.environ.copy()
        environment["LC_ALL"] = "C"
        return subprocess.run(
            [
                "sh",
                "-c",
                'set -e; . "$1"; cetirun_sync_builtin_skills "$2" "$3"',
                "sh",
                str(SYNC_SCRIPT),
                str(source),
                str(destination),
            ],
            check=False,
            capture_output=True,
            text=True,
            env=environment,
        )


if __name__ == "__main__":
    unittest.main()
