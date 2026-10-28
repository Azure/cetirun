# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Optional mount-level qualification using an explicitly selected local image.

Set CETIRUN_PI_TEST_IMAGE as well to require that same cached image to start
the Pi HTTP adapter after it reads the canonical context.
"""

import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]
CHART = ROOT / "app/cetirun-chart"
IMAGE = os.environ.get("CETIRUN_SKILLS_TEST_IMAGE")
PI_IMAGE = os.environ.get("CETIRUN_PI_TEST_IMAGE")


@unittest.skipUnless(IMAGE, "set CETIRUN_SKILLS_TEST_IMAGE to an already-cached shell image")
@unittest.skipIf(os.geteuid() == 0, "requires a non-root caller for the mount qualification")
class CustomSkillsContainerTests(unittest.TestCase):
    def test_common_context_materialization_and_readonly_agent_view(self):
        self.assertIsNotNone(shutil.which("docker"))
        image_id, platform = subprocess.check_output(
            ["docker", "image", "inspect", "--format", "{{.Id}} {{.Os}}/{{.Architecture}}", IMAGE],
            text=True,
            timeout=10,
        ).split()
        self.assertTrue(image_id.startswith("sha256:"))
        seed_image = f"fixture@{image_id}"
        with tempfile.TemporaryDirectory(prefix="cetirun-skills-mounts-") as directory:
            root = Path(directory)
            source, home, context, assets, shared, view = (
                root / name for name in ("source", "home", "context", "assets", "shared", "view")
            )
            for path in (source, home, context, assets, shared, view):
                path.mkdir()
            (source / "seed").write_text("synthetic image HOME")
            (context / "AGENTS.md").write_text("synthetic configured context")
            (assets / "manifest.json").write_text('{"version":"v1"}')
            (assets / "AGENTS.md").write_text("synthetic canonical memory")
            (assets / "skills/cetirun-memory").mkdir(parents=True)
            (assets / "skills/cetirun-memory/SKILL.md").write_text(
                "---\nname: cetirun-memory\ndescription: Synthetic memory context.\n---\nsynthetic memory\n"
            )
            (shared / "team").mkdir()
            (shared / "team/SKILL.md").write_text("synthetic shared skill")

            def mount(path, target, readonly=False):
                self.assertNotIn(",", str(path))
                return ["--mount", f"type=bind,src={path},dst={target}" + (",readonly" if readonly else "")]

            base = [
                "docker", "run", "--rm", "--pull=never", "--platform", platform, "--network=none", "--read-only",
                "--user", f"{os.getuid()}:{os.getgid()}", "--cap-drop=ALL",
                "--security-opt=no-new-privileges", "--entrypoint=/bin/sh",
            ]
            custom_init = base + mount(source, "/image-home", True) + mount(home, "/home-data")
            custom_init += mount(CHART / "configs/init-home-common.sh", "/etc/cetirun/init-home-common.sh", True)
            custom_init += mount(CHART / "agents/custom/bootstrap/init-home.sh", "/etc/cetirun/custom-init-home.sh", True)
            custom_init += [
                "-e", "SOURCE_HOME=/image-home", "-e", "TARGET_HOME=/home-data",
                "-e", f"CETIRUN_INIT_IMAGE={seed_image}",
                image_id, "/etc/cetirun/custom-init-home.sh",
            ]
            result = subprocess.run(custom_init, text=True, capture_output=True, timeout=30)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertTrue((home / ".agents").is_dir())
            self.assertFalse((view / "current").exists(), "Custom HOME initialization must not create a context view")

            # The platform-owned common initializer materializes the context
            # view. Custom's HOME initializer receives none of these sources.
            common_context = base + mount(home, "/home/worker") + mount(context, "/etc/cetirun-context", True)
            common_context += mount(assets, "/opt/cetirun-memory", True) + mount(shared, "/etc/cetirun-shared-skills", True)
            common_context += mount(view, "/etc/cetirun-context-view")
            common_context += mount(CHART / "configs/init-home-common.sh", "/etc/cetirun/init-home-common.sh", True)
            common_context += [
                image_id,
                "-ec",
                """
                    . /etc/cetirun/init-home-common.sh
                    TARGET_HOME=/home/worker
                    CETIRUN_CONTEXT_DIR=/etc/cetirun-context
                    CETIRUN_MEMORY_ASSETS=/opt/cetirun-memory
                    CETIRUN_SHARED_SKILLS_DIR=/etc/cetirun-shared-skills
                    CETIRUN_CONTEXT_VIEW=/etc/cetirun-context-view
                    prepare_agent_context
                """,
            ]
            result = subprocess.run(common_context, text=True, capture_output=True, timeout=30)
            self.assertEqual(result.returncode, 0, result.stderr)

            # Even without Memory or a shared PVC, Custom uses the same bounded
            # canonical view. That view owns an empty skills directory rather
            # than nesting a volume below a read-only ConfigMap projection.
            bare_context, bare_view = root / "bare-context", root / "bare-view"
            bare_context.mkdir()
            bare_view.mkdir()
            (bare_context / "AGENTS.md").write_text("synthetic Team-only context")
            bare = base + mount(home, "/home/worker") + mount(bare_context, "/etc/cetirun-context", True)
            bare += mount(bare_view, "/etc/cetirun-context-view")
            bare += mount(CHART / "configs/init-home-common.sh", "/etc/cetirun/init-home-common.sh", True)
            bare += [image_id, "-ec", """
                . /etc/cetirun/init-home-common.sh
                TARGET_HOME=/home/worker
                CETIRUN_CONTEXT_DIR=/etc/cetirun-context
                CETIRUN_CONTEXT_VIEW=/etc/cetirun-context-view
                prepare_agent_context
            """]
            result = subprocess.run(bare, text=True, capture_output=True, timeout=30)
            self.assertEqual(result.returncode, 0, result.stderr)
            bare_check = base + mount(home, "/home/worker") + mount(bare_view / "current", "/home/worker/.agents", True)
            bare_check += [image_id, "-ec", """
                grep -qx 'synthetic Team-only context' /home/worker/.agents/AGENTS.md
                test -d /home/worker/.agents/skills
            """]
            result = subprocess.run(bare_check, text=True, capture_output=True, timeout=30)
            self.assertEqual(result.returncode, 0, result.stderr)

            check = base + mount(home, "/home/worker") + mount(view / "current", "/home/worker/.agents", True)
            check += mount(shared, "/shared-source", True)
            check += [image_id, "-ec", """
                test "$(id -u)" -ne 0
                grep -qx 'synthetic configured context' /home/worker/.agents/AGENTS.md
                grep -qx 'synthetic canonical memory' /home/worker/.agents/AGENTS.md
                grep -qx 'synthetic memory' /home/worker/.agents/skills/cetirun-memory/SKILL.md
                test "$(cat /home/worker/.agents/skills/team/SKILL.md)" = "synthetic shared skill"
                if printf changed > /home/worker/.agents/write-probe; then exit 1; fi
                if printf changed > /shared-source/team/SKILL.md; then exit 1; fi
                mkdir -p /home/worker/memory
                printf kept > /home/worker/memory/private
            """]
            result = subprocess.run(check, text=True, capture_output=True, timeout=30)
            self.assertEqual(result.returncode, 0, result.stderr)
            if PI_IMAGE:
                pi_id = subprocess.check_output(
                    ["docker", "image", "inspect", "--format", "{{.Id}}", PI_IMAGE], text=True, timeout=10,
                ).strip()
                self.assertEqual(pi_id, image_id, "Pi startup must use the selected immutable context-test image")
                startup = base + mount(home, "/home/worker") + mount(view / "current", "/home/worker/.agents", True)
                startup += ["-e", "HOME=/home/worker"]
                startup += [image_id, "-ec", """
                    node --input-type=module -e 'import("/opt/cetirun-pi/pi-runtime.mjs").then(({createPiRuntime}) => createPiRuntime(process.env.HOME)).catch(error => { console.error(error.stack); process.exit(1) })'
                    PORT=8080 PI_OFFLINE=1 node /opt/cetirun-pi/server.mjs > /home/worker/pi-startup.log 2>&1 &
                    pi_pid=$!
                    for attempt in 1 2 3 4 5; do
                      kill -0 "$pi_pid" 2>/dev/null || { cat /home/worker/pi-startup.log; exit 1; }
                      grep -q 'Pi HTTP adapter ready' /home/worker/pi-startup.log && break
                      sleep 1
                    done
                    grep -q 'Pi HTTP adapter ready' /home/worker/pi-startup.log || { cat /home/worker/pi-startup.log; exit 1; }
                    node --input-type=module -e 'const response = await fetch("http://127.0.0.1:8080/healthz"); if (!response.ok || (await response.json()).status !== "ok") process.exit(1)'
                    kill "$pi_pid"
                    wait "$pi_pid" || true
                """]
                result = subprocess.run(startup, text=True, capture_output=True, timeout=30)
                self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
            result = subprocess.run(custom_init, text=True, capture_output=True, timeout=30)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual((home / "memory/private").read_text(), "kept")
            self.assertEqual((shared / "team/SKILL.md").read_text(), "synthetic shared skill")
            self.assertFalse((view / "current/write-probe").exists())


if __name__ == "__main__":
    unittest.main()
