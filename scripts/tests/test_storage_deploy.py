# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
"""Storage bootstrap target isolation and control-plane verification."""

import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
SUBSCRIPTION = "11111111-1111-1111-1111-111111111111"
TENANT = "22222222-2222-2222-2222-222222222222"
FAKE_AZ = '''#!/usr/bin/env python3
import json, os, pathlib, stat, sys
args = sys.argv[1:]
with open(os.environ['CALLS_LOG'], 'a') as log:
    print(json.dumps(args), file=log)
if args[:3] in (['deployment', 'group', 'validate'], ['deployment', 'group', 'create']):
    template = pathlib.Path(args[args.index('--template-file') + 1])
    parameter = args[args.index('--parameters') + 1]
    assert parameter.startswith('@')
    parameters = pathlib.Path(parameter[1:])
    assert json.loads(template.read_text()) == {}
    assert json.loads(parameters.read_text()) == {'parameters':{'marker':{'value':'original'}}}
    for path in [template, parameters]:
        assert stat.S_IMODE(path.stat().st_mode) == 0o600
    assert stat.S_IMODE(template.parent.stat().st_mode) == 0o700
if args[:2] == ['account', 'show']:
    if os.environ.get('MUTATE_STORAGE_PARAMETERS'):
        pathlib.Path(os.environ['PARAM_FILE']).write_text('{"parameters":{"marker":{"value":"changed"}}}')
    print(json.dumps({'id': os.environ['AZURE_SUBSCRIPTION_ID'], 'tenantId': 'wrong' if os.environ.get('WRONG_TENANT') else os.environ['AZURE_TENANT_ID']}))
elif args[:3] == ['deployment', 'group', 'show']:
    query = args[args.index('--query') + 1]
    print('https://storage.example.invalid' if 'Endpoint' in query else 'example-storage')
else:
    print('{}')
'''
FAKE_BICEP = '''#!/usr/bin/env python3
import json, os, pathlib, sys
if os.environ.get('FAIL_BICEP') == '1':
    sys.exit(1)
args = sys.argv[1:]
assert '--stdout' in args and '--no-restore' in args
if args[0] == 'build-params':
    assert pathlib.Path(args[1]).suffix == '.bicepparam'
    assert pathlib.Path(args[1]).read_text().startswith('using ')
    assert pathlib.Path(args[args.index('--bicep-file') + 1]).name == 'main.bicep'
    print(json.dumps({'templateJson': '{}', 'parametersJson': json.dumps({
        'parameters': {'marker': {'value': 'original'}}})}))
else:
    assert args[0] == 'build'
    print('{}')
'''


class StorageDeployTests(unittest.TestCase):
    def run_deploy(self, *, bicepparam=False, **overrides):
        with tempfile.TemporaryDirectory() as directory:
            tmp = Path(directory)
            (tmp / "az").write_text(FAKE_AZ)
            (tmp / "az").chmod(0o755)
            (tmp / "bicep").write_text(FAKE_BICEP)
            (tmp / "bicep").chmod(0o755)
            parameters = tmp / ("storage.bicepparam" if bicepparam else "storage.json")
            parameters.write_text("using './main.bicep'\nparam marker = 'original'\n" if bicepparam
                                  else '{"parameters":{"marker":{"value":"original"}}}')
            calls = tmp / "calls.jsonl"
            env = dict(os.environ, PATH=str(tmp) + os.pathsep + os.environ["PATH"],
                       CALLS_LOG=str(calls), RESOURCE_GROUP="example-rg", AKS_CLUSTER_NAME="example-cluster",
                       AZURE_SUBSCRIPTION_ID=SUBSCRIPTION, AZURE_TENANT_ID=TENANT,
                       AKS_SUBNET_ID=f"/subscriptions/{SUBSCRIPTION}/resourceGroups/example-nodes/providers/Microsoft.Network/virtualNetworks/example-vnet/subnets/aks-subnet",
                       PARAM_FILE=str(parameters))
            env.update(overrides)
            result = subprocess.run(["bash", str(ROOT / "infra/storage/deploy.sh")],
                                    env=env, capture_output=True, text=True)
            rows = [json.loads(line) for line in calls.read_text().splitlines()] if calls.exists() else []
            return result, rows

    def test_all_resource_calls_use_explicit_subscription(self):
        result, calls = self.run_deploy()
        self.assertEqual(result.returncode, 0, result.stderr)
        for args in calls:
            if args[:4] == ["network", "vnet", "subnet", "update"]:
                subnet = args[args.index("--ids") + 1]
                self.assertEqual(subnet.split("/")[2], SUBSCRIPTION)
            else:
                self.assertEqual(args[args.index("--subscription") + 1], SUBSCRIPTION)
        self.assertFalse(any(args[:2] == ["account", "set"] for args in calls))

    def test_subnet_restricted_storage_is_verified_through_arm(self):
        result, calls = self.run_deploy()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(any(args[:3] == ["storage", "container-rm", "show"] for args in calls))
        self.assertFalse(any(args[:2] == ["storage", "container"] for args in calls))

    def test_private_endpoint_storage_uses_the_same_arm_verification(self):
        result, calls = self.run_deploy(PUBLIC_NETWORK_ACCESS="Disabled", PRIVATE_ENDPOINT_SUBNET_ID=f"/subscriptions/{SUBSCRIPTION}/resourceGroups/example-rg/providers/Microsoft.Network/virtualNetworks/example/subnets/private")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(any(args[:3] == ["storage", "container-rm", "show"] for args in calls))
        self.assertFalse(any(args[:4] == ["network", "vnet", "subnet", "update"] for args in calls))

    def test_wrong_tenant_stops_before_any_write(self):
        result, calls = self.run_deploy(WRONG_TENANT="1")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(len(calls), 1)
        self.assertEqual(calls[0][:2], ["account", "show"])

    def test_invalid_local_inputs_stop_before_subnet_changes(self):
        for bicepparam in (False, True):
            with self.subTest(bicepparam=bicepparam):
                result, calls = self.run_deploy(bicepparam=bicepparam, FAIL_BICEP="1")
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(calls, [])

    def test_foreign_or_malformed_subnet_cannot_mutate_same_name_in_target(self):
        for subnet in (
            f"/subscriptions/{TENANT}/resourceGroups/example-nodes/providers/Microsoft.Network/virtualNetworks/example-vnet/subnets/aks-subnet",
            f"/subscriptions/{SUBSCRIPTION}/resourceGroups/example-nodes/providers/Microsoft.Network/virtualNetworks/example-vnet/subnets/",
            f"/subscriptions/{SUBSCRIPTION}/resourceGroups/example-nodes/providers/MicrosoftXNetwork/virtualNetworks/example-vnet/subnets/aks-subnet",
            f"/subscriptions/{SUBSCRIPTION}/resourceGroups/example-nodes/providers/Microsoft-Network/virtualNetworks/example-vnet/subnets/aks-subnet",
        ):
            with self.subTest(subnet=subnet):
                result, calls = self.run_deploy(AKS_SUBNET_ID=subnet)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("subnet ARM ID", result.stderr)
                self.assertFalse(any(args[:3] == ["network", "vnet", "subnet"] for args in calls))
                self.assertFalse(any(args[:2] == ["deployment", "group"] for args in calls))

    def test_subnet_update_and_deployment_use_the_same_resource_id(self):
        for subnet in (
            f"/subscriptions/{SUBSCRIPTION}/resourceGroups/example-nodes/providers/Microsoft.Network/virtualNetworks/example-vnet/subnets/aks-subnet",
            f"/subscriptions/{SUBSCRIPTION}/resourceGroups/example-nodes/providers/Microsoft.Network/virtualNetworks/example-vnet/Subnets/aks-subnet",
            f"/SUBSCRIPTIONS/{SUBSCRIPTION}/RESOURCEGROUPS/Example-Nodes/PROVIDERS/microsoft.network/VIRTUALNETWORKS/Example-VNet/SUBNETS/Example-Subnet",
        ):
            with self.subTest(subnet=subnet):
                result, calls = self.run_deploy(AKS_SUBNET_ID=subnet)
                self.assertEqual(result.returncode, 0, result.stderr)
                updates = [args for args in calls if args[:4] == ["network", "vnet", "subnet", "update"]]
                self.assertEqual(len(updates), 1)
                self.assertEqual(updates[0][updates[0].index("--ids") + 1], subnet)
                for option in ("--resource-group", "--vnet-name", "--name", "--subscription"):
                    self.assertNotIn(option, updates[0])
                deployments = [args for args in calls if args[:2] == ["deployment", "group"] and args[2] in ("validate", "create")]
                self.assertEqual([args[2] for args in deployments], ["validate", "create"])
                for args in deployments:
                    allowed = [value.split("=", 1)[1] for value in args if value.startswith("allowedSubnetIds=")]
                    self.assertEqual([json.loads(value) for value in allowed], [[subnet]])

    def test_deployment_keeps_compiled_snapshot_after_parameter_source_changes(self):
        result, calls = self.run_deploy(MUTATE_STORAGE_PARAMETERS="1")
        self.assertEqual(result.returncode, 0, result.stderr)
        deployments = [args for args in calls if args[:2] == ["deployment", "group"] and args[2] in ("validate", "create")]
        self.assertEqual(len(deployments), 2)
        self.assertEqual(deployments[0][deployments[0].index("--template-file") + 1],
                         deployments[1][deployments[1].index("--template-file") + 1])

    def test_bicep_parameters_use_the_same_private_snapshots_after_source_changes(self):
        result, calls = self.run_deploy(bicepparam=True, MUTATE_STORAGE_PARAMETERS="1")
        self.assertEqual(result.returncode, 0, result.stderr)
        deployments = [args for args in calls if args[:2] == ["deployment", "group"] and args[2] in ("validate", "create")]
        self.assertEqual([args[2] for args in deployments], ["validate", "create"])
        for option in ("--template-file", "--parameters"):
            first = deployments[0][deployments[0].index(option) + 1]
            second = deployments[1][deployments[1].index(option) + 1]
            self.assertEqual(first, second)
            self.assertTrue(first.endswith(".json"))
