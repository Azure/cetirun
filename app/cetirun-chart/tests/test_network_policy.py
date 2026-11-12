# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
"""Offline renders of the egress defaults of standard Agents."""

import unittest

from test_cetiadmin import BLOCKED, objects, ordinary_values, render


def namespace(name):
    return {"namespaceSelector": {"matchLabels": {"kubernetes.io/metadata.name": name}}}


DNS_PODS = {**namespace("kube-system"), "podSelector": {"matchLabels": {"k8s-app": "kube-dns"}}}
DNS_PORTS = [{"protocol": "UDP", "port": 53}, {"protocol": "TCP", "port": 53}]
ISTIOD = namespace("istio-system")
GATEWAY = {**namespace("cetirun-system"), "podSelector": {"matchLabels": {
    "app.kubernetes.io/name": "cetirun-ai-gateway", "app.kubernetes.io/component": "gateway"}}}


def is_public(rule):
    return any("ipBlock" in peer for peer in rule["to"])


class NetworkPolicyDefaultsTests(unittest.TestCase):
    def policies(self, values):
        result = render(values)
        self.assertEqual(result.returncode, 0, result.stderr)
        return {item["metadata"]["labels"].get("app.kubernetes.io/component"): item["spec"]
                for item in objects(result) if item["kind"] == "NetworkPolicy"}

    def test_in_cluster_egress_is_dns_pods_and_platform_services(self):
        for agent_type in ("hermes", "openclaw"):
            with self.subTest(agent_type=agent_type):
                values = ordinary_values()
                values["agentType"] = agent_type
                egress = self.policies(values)[None]["egress"]
                internal = [rule for rule in egress if not is_public(rule)]
                self.assertEqual([rule["to"] for rule in internal], [[DNS_PODS], [ISTIOD], [GATEWAY]])
                self.assertEqual(internal[0]["ports"], DNS_PORTS)
                public = [rule for rule in egress if is_public(rule)]
                self.assertEqual(len(public), 1, "the default render allows an internal address")
                self.assertEqual(public[0]["to"], [{"ipBlock": {"cidr": "0.0.0.0/0", "except": BLOCKED}}])

    def test_copilot_proxy_uses_the_same_dns_rule_and_blocks(self):
        values = ordinary_values()
        values["aiGateway"]["copilotProxy"] = {
            "enabled": True, "image": "registry.example.com/cetirun-ai-gateway:0.1.1",
            "masterKeySecretName": "copilot-proxy-key", "tokenSecretName": "copilot-token"}
        egress = self.policies(values)["copilot-proxy"]["egress"]
        self.assertEqual([rule["to"] for rule in egress if rule["ports"] == DNS_PORTS], [[DNS_PODS]])
        public = [rule for rule in egress if is_public(rule)]
        self.assertEqual([rule["to"][0]["ipBlock"]["except"] for rule in public], [BLOCKED])

    def test_empty_dns_pod_label_drops_the_default_label(self):
        for labels, peer in (({"k8s-app": "", "app": "coredns"}, {**namespace("kube-system"), "podSelector": {"matchLabels": {"app": "coredns"}}}),
                             ({"k8s-app": ""}, namespace("kube-system"))):
            with self.subTest(labels=labels):
                values = ordinary_values()
                values["networkPolicy"] = {"dnsPodLabels": labels}
                egress = self.policies(values)[None]["egress"]
                self.assertEqual([rule["to"] for rule in egress if rule.get("ports") == DNS_PORTS], [[peer]])


if __name__ == "__main__":
    unittest.main()
