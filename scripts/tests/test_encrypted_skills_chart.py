# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
"""Encrypted shared storage contracts in the standalone Agent chart."""

import copy
import shutil
import unittest

from scripts.tests.test_native_gateway_chart import RUNTIME_CHART, native_values, render


@unittest.skipUnless(shutil.which("helm"), "requires Helm 3 or 4")
class EncryptedSkillsChartTests(unittest.TestCase):
    def values(self):
        values = native_values()
        values.pop("sandboxWorkload")
        values["sidecarScope"] = {"enabled": True}
        values["skills"] = {
            "shared": True, "backend": "azure-files-nfs", "containerName": "",
            "shareName": "shared-skills", "resourceGroup": "example-rg",
            "storageAccount": "exampleskills", "storageClassName": "azurefile-csi-premium",
        }
        return values

    def test_reader_is_encrypted_read_only_and_keyless(self):
        objects = render(RUNTIME_CHART, self.values())
        pv = next(doc["spec"] for doc in objects if doc["kind"] == "PersistentVolume")
        self.assertEqual(pv["mountOptions"], ["ro"])
        self.assertEqual(pv["csi"]["driver"], "file.csi.azure.com")
        self.assertIs(pv["csi"]["readOnly"], True)
        self.assertNotIn("nodeStageSecretRef", pv["csi"])
        self.assertEqual(pv["csi"]["volumeAttributes"], {
            "resourceGroup": "example-rg", "storageAccount": "exampleskills", "shareName": "shared-skills",
            "protocol": "nfs", "encryptInTransit": "true", "mountPermissions": "0", "fsGroupChangePolicy": "None"})

    def test_unsafe_overrides_fail_rendering(self):
        for field, value in (("backend", "other"), ("shareName", ""), ("containerName", "blob-skills"),
                             ("secretName", "account-key"), ("mountOptions", ["rw"]), ("protocol", "smb"),
                             ("encryptInTransit", False), ("volumeAttributes", {"encryptInTransit": "false"})):
            with self.subTest(field=field):
                values = copy.deepcopy(self.values())
                values["skills"][field] = value
                render(RUNTIME_CHART, values, error="skills")

    def test_existing_claim_does_not_create_another_volume(self):
        values = self.values()
        values["skills"]["existingClaim"] = "approved-skills"
        self.assertFalse([doc for doc in render(RUNTIME_CHART, values) if doc["kind"] == "PersistentVolume"])
