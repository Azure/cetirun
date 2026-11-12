# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
"""The check of the pinned ADX-Mon images, offline: no Helm, Docker or registry."""

from contextlib import redirect_stderr, redirect_stdout
import io
from pathlib import Path
import subprocess
import sys
import textwrap
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import check_observability_images as check


COLLECTOR = "mcr.example.com/adx-mon/collector:v1@sha256:" + "1" * 64
KSM = "mcr.example.com/kube-state-metrics:v2@sha256:" + "2" * 64
RENDERED = textwrap.dedent(f"""\
    kind: DaemonSet
    metadata:
      name: collector
    spec:
      template:
        spec:
          containers:
            - name: collector
              image: "{COLLECTOR}"
              command: ["/collector"]
              args: ["--config=/etc/config.toml", "--hostname=$(NODE_NAME)", "--host-ip=$(HOST_IP)"]
    ---
    kind: Deployment
    metadata:
      name: telemetry
    spec:
      template:
        spec:
          containers:
            - name: telemetry
              image: "registry.example.com/cetirun-operator:0.1.1"
              command: ["/telemetry"]
              args: ["--events=all"]
    ---
    kind: Deployment
    metadata:
      name: kube-state-metrics
    spec:
      template:
        spec:
          containers:
            - name: kube-state-metrics
              image: "{KSM}"
              args: ["--resources=pods", "--port=8080"]
    ---
    kind: ConfigMap
    metadata:
      name: other
    """)
# The usage of an older collector, which predates --host-ip.
OLD_COLLECTOR_USAGE = """\
GLOBAL OPTIONS:
   --hostname value  Hostname filter override
   --config value    Config file path
   --help, -h        show help
"""
KSM_USAGE = "      --port int         Port to expose metrics on. (default 8080)\n      --resources string  Resources\n"


class PinnedImageTests(unittest.TestCase):
    def test_images_group_the_flags_of_each_program_and_skip_cetirun_images(self):
        self.assertEqual(check.pinned_images(RENDERED), {
            COLLECTOR: {"/collector": {"--config", "--hostname", "--host-ip"}},
            KSM: {None: {"--resources", "--port"}},
        })

    def test_an_image_without_a_digest_is_refused(self):
        with self.assertRaisesRegex(ValueError, "is not pinned by digest"):
            check.pinned_images(RENDERED.replace(f"@sha256:{'2' * 64}", ""))

    def test_a_flag_is_known_only_by_its_whole_name(self):
        self.assertEqual(check.unknown_flags(OLD_COLLECTOR_USAGE, {"--config", "--hostname", "--host-ip"}), ["--host-ip"])
        self.assertEqual(check.unknown_flags(OLD_COLLECTOR_USAGE, {"--host", "--conf"}), ["--conf", "--host"])
        self.assertEqual(check.unknown_flags(KSM_USAGE, {"--port", "--resources"}), [])

    def test_unknown_flags_and_cves_fail_after_every_image_is_checked(self):
        def docker(command, **_kwargs):
            usage = OLD_COLLECTOR_USAGE if "/collector" in command else KSM_USAGE
            return subprocess.CompletedProcess(command, 0, stdout=usage, stderr="")

        scanned = []

        def scan(image):
            scanned.append(image)
            if image == KSM:
                raise subprocess.CalledProcessError(1, "trivy")

        with patch.object(check, "run", return_value=RENDERED) as run, \
                patch.object(check.subprocess, "run", side_effect=docker), \
                patch.object(check, "scan", side_effect=scan), \
                redirect_stderr(io.StringIO()) as errors, redirect_stdout(io.StringIO()):
            self.assertEqual(check.main(), 1)
        self.assertEqual(scanned, [COLLECTOR, KSM])
        pulled = [call.args[0][-1] for call in run.call_args_list if call.args[0][:2] == ["docker", "pull"]]
        self.assertEqual(pulled, [COLLECTOR, KSM])
        self.assertIn(f"{COLLECTOR} /collector does not accept --host-ip", errors.getvalue())
        self.assertIn(f"{KSM} did not pass the CVE gate", errors.getvalue())

    def test_known_flags_and_clean_images_pass(self):
        def docker(command, **_kwargs):
            usage = OLD_COLLECTOR_USAGE + "   --host-ip value  Host IP\n" if "/collector" in command else KSM_USAGE
            return subprocess.CompletedProcess(command, 0, stdout=usage, stderr="")

        with patch.object(check, "run", return_value=RENDERED), \
                patch.object(check.subprocess, "run", side_effect=docker), \
                patch.object(check, "scan"), redirect_stdout(io.StringIO()):
            self.assertEqual(check.main(), 0)



if __name__ == "__main__":
    unittest.main()
