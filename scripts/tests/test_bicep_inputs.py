# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
"""Shared offline Bicep input decoding and private snapshot regressions."""

from contextlib import redirect_stderr
import io
import json
import os
from pathlib import Path
import stat
import subprocess
import tempfile
import unittest
from unittest.mock import patch

from scripts import bicep_inputs


class BicepInputsTests(unittest.TestCase):
    def test_bicep_bundle_and_json_inputs_preserve_the_same_documents(self):
        template = {"resources": []}
        parameters = {"parameters": {"marker": {"value": "synthetic-value"}}}
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / "main.bicep"
            for suffix in (".bicepparam", ".json"):
                with self.subTest(suffix=suffix):
                    parameter_file = root / f"example{suffix}"
                    parameter_file.write_text(json.dumps(parameters), encoding="utf-8")
                    compiler_output = json.dumps({
                        "templateJson": json.dumps(template),
                        "parametersJson": json.dumps(parameters),
                    } if suffix == ".bicepparam" else template)
                    with patch.object(bicep_inputs.subprocess, "run", return_value=
                                      subprocess.CompletedProcess([], 0, compiler_output)) as compiler:
                        compiled = bicep_inputs.compile_inputs(source, parameter_file)
                    self.assertEqual(compiled, (template, parameters))
                    command = compiler.call_args.args[0]
                    self.assertIn("--no-restore", command)
                    if suffix == ".bicepparam":
                        self.assertEqual(command[1:5], ["build-params", str(parameter_file),
                                                       "--bicep-file", str(source)])
                    else:
                        self.assertEqual(command[1:3], ["build", str(source)])

    def test_invalid_compiler_output_cannot_create_snapshots_or_print_values(self):
        sentinel = "synthetic-private-value"
        for bundle in (
            [], {},
            {"templateJson": sentinel, "parametersJson": "{}"},
            {"templateJson": "[]", "parametersJson": '{"parameters":{}}'},
            {"templateJson": "{}", "parametersJson": sentinel},
            {"templateJson": "{}", "parametersJson": "[]"},
            {"templateJson": "{}", "parametersJson": "{}"},
            {"templateJson": "{}", "parametersJson": '{"parameters":[]}'},
        ):
            with self.subTest(bundle=bundle), tempfile.TemporaryDirectory() as directory:
                errors = io.StringIO()
                with patch.object(bicep_inputs.subprocess, "run", return_value=
                                  subprocess.CompletedProcess([], 0, json.dumps(bundle))), redirect_stderr(errors):
                    result = bicep_inputs.main(["--template", "main.bicep", "--parameters", "example.bicepparam",
                                                "--output-dir", directory])
                self.assertEqual(result, 1)
                self.assertNotIn(sentinel, errors.getvalue())
                self.assertNotIn("Traceback", errors.getvalue())
                self.assertEqual(list(Path(directory).iterdir()), [])

    def test_snapshots_are_private_even_with_a_permissive_umask(self):
        documents = ({"resources": []}, {"parameters": {}})
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            previous_umask = os.umask(0)
            try:
                bicep_inputs.write_snapshots(root, *documents)
            finally:
                os.umask(previous_umask)
            for name, document in zip(("template", "parameters"), documents):
                path = root / f"{name}.json"
                self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)
                self.assertEqual(json.loads(path.read_text(encoding="utf-8")), document)

    def test_snapshots_never_overwrite_existing_files_or_symlink_targets(self):
        for filename in ("template.json", "parameters.json"):
            for symlink in (False, True):
                with self.subTest(filename=filename, symlink=symlink), tempfile.TemporaryDirectory() as directory:
                    root = Path(directory)
                    existing = root / filename
                    target = root / "target.json" if symlink else existing
                    target.write_text("original", encoding="utf-8")
                    if symlink:
                        existing.symlink_to(target)
                    with self.assertRaises(FileExistsError):
                        bicep_inputs.write_snapshots(root, {}, {"parameters": {}})
                    self.assertEqual(target.read_text(encoding="utf-8"), "original")
