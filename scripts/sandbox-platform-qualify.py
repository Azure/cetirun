#!/usr/bin/env python3
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
"""Explicit-target preflight and bounded Kata/Cilium network smoke tests.

This does not register capabilities, authorize execution, or prove writer fencing.
"""

import argparse
import ipaddress
import json
import re
import subprocess
import sys
import time
import uuid


RUN_LABEL = "qualification.cetirun.microsoft.com/run"
PROBE = (
    "import http.client,json,sys\n"
    "results={}\n"
    "for port in (8082,8080,8081):\n"
    " c=http.client.HTTPConnection(sys.argv[1],port,timeout=2)\n"
    " try:\n"
    "  try: c.connect()\n"
    "  except TimeoutError: results[port]=False; continue\n"
    "  c.request('GET','/'); r=c.getresponse()\n"
    "  if r.status!=200 or r.read()!=b'qualification-ok': raise SystemExit('unexpected synthetic server response')\n"
    "  results[port]=True\n"
    " finally: c.close()\n"
    "print(json.dumps([results[port] for port in (8080,8081,8082)]))\n"
)
SERVER = (
    "from http.server import BaseHTTPRequestHandler,HTTPServer\n"
    "import ipaddress,os,socket,threading,time\n"
    "address=ipaddress.ip_address(os.environ['POD_IP'])\n"
    "class Server(HTTPServer):\n"
    " address_family=socket.AF_INET6 if address.version==6 else socket.AF_INET\n"
    "class Handler(BaseHTTPRequestHandler):\n"
    " def do_GET(self):\n"
    "  self.send_response(200);self.end_headers();self.wfile.write(b'qualification-ok')\n"
    " def log_message(self,*args): pass\n"
    "for port in (8080,8081,8082):\n"
    " threading.Thread(target=Server((str(address),port),Handler).serve_forever,daemon=True).start()\n"
    "time.sleep(3600)\n"
)
LISTENER_READINESS = (
    "import os,socket\n"
    "for port in (8080,8081,8082):\n"
    " with socket.create_connection((os.environ['POD_IP'],port),timeout=1): pass\n"
)


class QualificationError(Exception):
    pass


class Kubernetes:
    def __init__(self, kubeconfig, context, cluster_uid):
        if not kubeconfig or not context or not cluster_uid:
            raise QualificationError("An explicit kubeconfig, context and cluster UID are required")
        self.prefix = [
            "kubectl", "--kubeconfig", kubeconfig, "--context", context,
            "--request-timeout=15s",
        ]
        self.cluster_uid = cluster_uid

    def run(self, args, document=None, timeout=30):
        try:
            result = subprocess.run(
                self.prefix + args,
                input=None if document is None else json.dumps(document),
                text=True, capture_output=True, timeout=timeout, check=False,
            )
        except (OSError, subprocess.TimeoutExpired) as error:
            raise QualificationError("Explicit-target Kubernetes command unavailable or timed out") from error
        if result.returncode:
            # kubectl diagnostics may include endpoints or authentication data.
            raise QualificationError(f"Kubernetes {args[0]} failed (exit {result.returncode}); no result accepted")
        return result.stdout

    def get(self, resource, name, namespace=None, absent_ok=False):
        args = ["get", resource, name, "-o", "json"]
        if namespace:
            args += ["--namespace", namespace]
        if absent_ok:
            args.append("--ignore-not-found")
        raw = self.run(args)
        if absent_ok and not raw.strip():
            return None
        try:
            result = json.loads(raw)
        except json.JSONDecodeError as error:
            raise QualificationError("Kubernetes returned an invalid JSON object") from error
        if not isinstance(result, dict):
            raise QualificationError("Kubernetes returned an unexpected object shape")
        return result

    def check_target(self):
        observed = self.get("namespace", "kube-system")
        if observed.get("metadata", {}).get("uid") != self.cluster_uid:
            raise QualificationError("Cluster UID mismatch; no test resources may be changed")

    def create(self, document):
        raw = self.run(["create", "-f", "-", "-o", "json"], document)
        try:
            result = json.loads(raw)
        except json.JSONDecodeError as error:
            raise QualificationError("Resource create returned invalid JSON; creation outcome is unknown") from error
        if not isinstance(result, dict):
            raise QualificationError("Resource create returned an unexpected shape; creation outcome is unknown")
        return result


def preflight(kube, runtime_class, runtime_handler, storage_class):
    kube.check_target()
    try:
        version = json.loads(kube.run(["get", "--raw", "/version"]))["gitVersion"]
    except (json.JSONDecodeError, KeyError) as error:
        raise QualificationError("Kubernetes version response is incomplete") from error
    match = re.fullmatch(r"v(\d+)\.(\d+)\.(\d+)(?:[-+].*)?", version)
    if not match or int(match[1]) != 1 or int(match[2]) < 37:
        raise QualificationError("Sandbox admission requires the qualified Kubernetes 1.37-or-newer API")
    runtime = kube.get("runtimeclass", runtime_class)
    if runtime.get("handler") != runtime_handler or runtime.get("metadata", {}).get("deletionTimestamp"):
        raise QualificationError("RuntimeClass handler mismatch or terminating RuntimeClass")
    storage = kube.get("storageclass", storage_class)
    if not storage.get("provisioner") or storage.get("metadata", {}).get("deletionTimestamp"):
        raise QualificationError("Selected storage prerequisite is incomplete or terminating")
    for resource in ("ciliumnetworkpolicies.cilium.io", "ciliumendpoints.cilium.io"):
        crd = kube.get("crd", resource)
        if not any(c.get("type") == "Established" and c.get("status") == "True"
                   for c in crd.get("status", {}).get("conditions", [])):
            raise QualificationError("Required Cilium API is not established")
    return {
        "kubernetesVersion": version,
        "runtimeClassHandlerMatched": True,
        "storageClassPresent": True,
        "ciliumAPIsEstablished": True,
    }


def pod(namespace, run, role, image, runtime_class):
    spec = {
        "automountServiceAccountToken": False, "restartPolicy": "Never",
        "securityContext": {"runAsNonRoot": True, "runAsUser": 65532, "runAsGroup": 65532},
        "containers": [{
            "name": role, "image": image, "imagePullPolicy": "IfNotPresent",
            "command": ["python3", "-c", SERVER if role == "server" else "import time;time.sleep(3600)"],
            "securityContext": {
                "allowPrivilegeEscalation": False, "readOnlyRootFilesystem": True,
                "capabilities": {"drop": ["ALL"]}, "seccompProfile": {"type": "RuntimeDefault"},
            },
            "resources": {
                "requests": {"cpu": "100m", "memory": "128Mi"},
                "limits": {"cpu": "1", "memory": "512Mi"},
            },
        }],
    }
    if role == "probe":
        spec["runtimeClassName"] = runtime_class
    else:
        spec["containers"][0]["env"] = [{
            "name": "POD_IP", "valueFrom": {"fieldRef": {"fieldPath": "status.podIP"}},
        }]
        spec["containers"][0]["readinessProbe"] = {
            "exec": {"command": ["python3", "-c", LISTENER_READINESS]},
            "periodSeconds": 1, "timeoutSeconds": 5, "failureThreshold": 3,
        }
    return {
        "apiVersion": "v1", "kind": "Pod",
        "metadata": {"name": role, "namespace": namespace, "labels": {RUN_LABEL: run, "role": role}},
        "spec": spec,
    }


def verify_pod_identity(value, expected_uid):
    metadata = value.get("metadata", {})
    if metadata.get("uid") != expected_uid or metadata.get("deletionTimestamp"):
        raise QualificationError("Synthetic Pod identity changed or is terminating")


def wait_ready(kube, namespace, name, expected_uid, timeout=180):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        value = kube.get("pod", name, namespace)
        verify_pod_identity(value, expected_uid)
        if value.get("status", {}).get("phase") in ("Failed", "Succeeded"):
            raise QualificationError("Synthetic probe exited before its readiness check")
        if any(c.get("type") == "Ready" and c.get("status") == "True"
               for c in value.get("status", {}).get("conditions", [])):
            return value
        time.sleep(1)
    raise QualificationError("Synthetic probe readiness timed out")


def connectivity(kube, namespace, address):
    try:
        ipaddress.ip_address(address)
    except ValueError as error:
        raise QualificationError("Synthetic server has an invalid Pod IP") from error
    raw = kube.run(["exec", "--namespace", namespace, "probe", "--", "python3", "-c", PROBE, address])
    try:
        result = json.loads(raw)
    except json.JSONDecodeError as error:
        raise QualificationError("Network probe output was not valid JSON") from error
    if not isinstance(result, list) or len(result) != 3 or any(type(v) is not bool for v in result):
        raise QualificationError("Network probe output had an unexpected shape")
    return result


def wait_connectivity(kube, namespace, address, expected, timeout=45):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if connectivity(kube, namespace, address) == expected:
            return
        time.sleep(1)
    raise QualificationError("Network behavior did not match the expected allow/deny result")


def cleanup(kube, namespace, uid, run, timeout=120):
    kube.check_target()
    current = kube.get("namespace", namespace, absent_ok=True)
    if current is None:
        return
    metadata = current.get("metadata", {})
    if metadata.get("uid") != uid or metadata.get("labels", {}).get(RUN_LABEL) != run:
        raise QualificationError("Cleanup ownership mismatch; namespace retained")
    if not metadata.get("resourceVersion"):
        raise QualificationError("Cleanup resource version is missing; namespace retained")
    kube.run(
        ["delete", "--raw", "/api/v1/namespaces/" + namespace, "-f", "-"],
        {"apiVersion": "v1", "kind": "DeleteOptions",
         "preconditions": {"uid": uid, "resourceVersion": metadata["resourceVersion"]}},
    )
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        current = kube.get("namespace", namespace, absent_ok=True)
        if current is None:
            return
        if current.get("metadata", {}).get("uid") != uid:
            raise QualificationError("Namespace was replaced during cleanup; replacement retained")
        time.sleep(1)
    raise QualificationError("Cleanup timed out; no finalizers were removed")


def network_smoke(kube, image, runtime_class, result):
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9./:_-]*@sha256:[0-9a-f]{64}", image):
        raise QualificationError("Network probes require an explicit digest-pinned image with Python 3")
    run = uuid.uuid4().hex
    namespace = "cetirun-sandbox-probe-" + run[:12]
    result["testNamespace"] = namespace
    result["cleanupComplete"] = False
    kube.check_target()
    created = kube.create({
        "apiVersion": "v1", "kind": "Namespace",
        "metadata": {"name": namespace, "labels": {RUN_LABEL: run}},
    })
    uid = created.get("metadata", {}).get("uid")
    if not uid:
        raise QualificationError("Namespace creation identity is unknown; do not adopt or delete by name")
    failure = None
    try:
        pod_uids = {}
        for role in ("server", "probe"):
            created_pod = kube.create(pod(namespace, run, role, image, runtime_class))
            pod_uid = created_pod.get("metadata", {}).get("uid")
            if not isinstance(pod_uid, str) or not pod_uid.strip():
                raise QualificationError("Synthetic Pod creation identity is unknown; no test results may be accepted")
            pod_uids[role] = pod_uid
        server = wait_ready(kube, namespace, "server", pod_uids["server"])
        wait_ready(kube, namespace, "probe", pod_uids["probe"])
        address = server.get("status", {}).get("podIP", "")
        if not address:
            raise QualificationError("Synthetic server has no Pod IP")
        wait_connectivity(kube, namespace, address, [True, True, True])
        result["networkChecks"] = {"baselineAllPorts": True}
        selector = {"matchLabels": {RUN_LABEL: run, "role": "probe"}}
        kube.create({
            "apiVersion": "cilium.io/v2", "kind": "CiliumNetworkPolicy",
            "metadata": {"name": "probe-deny", "namespace": namespace},
            "spec": {"endpointSelector": selector,
                     "egress": [{"toEntities": ["all"],
                                 "toPorts": [{"ports": [{"port": "8081", "protocol": "TCP"}]}]}],
                     "egressDeny": [{"toEntities": ["all"],
                                     "toPorts": [{"ports": [{"port": "8080", "protocol": "TCP"}]}]}]},
        })
        wait_connectivity(kube, namespace, address, [False, True, False])
        result["networkChecks"]["destinationPortDeny"] = True
        result["networkChecks"]["witnessInitiallyBlocked"] = True
        kube.create({
            "apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
            "metadata": {"name": "overlapping-allow", "namespace": namespace},
            "spec": {"podSelector": selector, "policyTypes": ["Egress"], "egress": [{}]},
        })
        # The probe tests the 8082 allow witness before the 8080 deny, so a
        # sample straddling policy application cannot reuse an earlier denial.
        wait_connectivity(kube, namespace, address, [False, True, True])
        result["networkChecks"]["overlappingAllowObserved"] = True
        result["networkChecks"]["denyOverOverlappingAllow"] = True
        for role, pod_uid in pod_uids.items():
            verify_pod_identity(kube.get("pod", role, namespace), pod_uid)
        result["networkChecks"]["unchangedServerIdentity"] = True
        result["networkChecks"]["unchangedProbeIdentity"] = True
    except QualificationError as error:
        failure = error
        raise
    finally:
        try:
            cleanup(kube, namespace, uid, run)
        except QualificationError as error:
            if failure is not None:
                raise QualificationError(f"Smoke failed: {failure}; cleanup failed: {error}") from error
            raise
        result["cleanupComplete"] = True


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=("preflight", "network-smoke"))
    parser.add_argument("--kubeconfig", required=True)
    parser.add_argument("--context", required=True)
    parser.add_argument("--cluster-uid", required=True, help="Expected kube-system namespace UID")
    parser.add_argument("--runtime-class", default="kata-vm-isolation")
    parser.add_argument("--runtime-handler", default="kata")
    parser.add_argument("--storage-class", required=True)
    parser.add_argument("--probe-image")
    parser.add_argument("--allow-test-resources", action="store_true")
    args = parser.parse_args(argv)
    result = {"version": "v1", "mode": args.mode, "status": "blocked", "qualifiesExecution": False}
    try:
        if args.mode == "network-smoke" and (not args.allow_test_resources or not args.probe_image):
            raise QualificationError("Network smoke requires explicit test-resource permission and a pinned probe image")
        kube = Kubernetes(args.kubeconfig, args.context, args.cluster_uid)
        result["preflight"] = preflight(kube, args.runtime_class, args.runtime_handler, args.storage_class)
        if args.mode == "network-smoke":
            network_smoke(kube, args.probe_image, args.runtime_class, result)
        result["status"] = "completed"
    except (QualificationError, ValueError) as error:
        result["error"] = str(error)
        print(json.dumps(result, sort_keys=True))
        return 1
    print(json.dumps(result, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main())
