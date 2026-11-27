# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

import ssl
import time
import unittest
from pathlib import Path
from unittest.mock import Mock

import sys
sys.path.insert(0, str(Path(__file__).parents[1]))
import verify_external_tls as verifier


class FakeTLS:
    def __init__(self, certificate, der):
        self.certificate = certificate
        self.der = der
        self.server_hostname = None

    def __enter__(self):
        return self

    def __exit__(self, *args):
        return False

    def getpeercert(self, binary_form=False):
        return self.der if binary_form else self.certificate


class FakeRaw:
    def __enter__(self):
        return self

    def __exit__(self, *args):
        return False


class ExternalTlsTests(unittest.TestCase):
    def setUp(self):
        self.der = b"certificate bytes"
        self.certificate = {
            "serialNumber": "01AB",
            "subjectAltName": (("DNS", "cetirun.example.com"), ("IP Address", "192.0.2.1")),
            "notAfter": "Jan 01 00:00:00 2030 GMT",
        }
        self.raw = FakeRaw()
        self.tls = FakeTLS(self.certificate, self.der)
        self.context = Mock()
        self.context.wrap_socket.return_value = self.tls
        self.connect = Mock(return_value=self.raw)

    def test_uses_direct_address_but_hostname_as_sni_and_verifies(self):
        result = verifier.verify_tls("cetirun.example.com", "192.0.2.1", now=time.time(), context_factory=lambda: self.context, connection_factory=self.connect)
        self.connect.assert_called_once_with(("192.0.2.1", 443), timeout=verifier.DEFAULT_TIMEOUT)
        self.context.wrap_socket.assert_called_once_with(self.raw, server_hostname="cetirun.example.com")
        self.assertEqual(self.context.verify_mode, ssl.CERT_REQUIRED)
        self.assertTrue(self.context.check_hostname)
        self.assertEqual(result["sanDnsNames"], ["cetirun.example.com"])

    def test_fingerprint_and_expiry_failures_are_reported(self):
        result = verifier.verify_tls("cetirun.example.com", expected_sha256="00" * 32, now=time.time(), context_factory=lambda: self.context, connection_factory=self.connect)
        self.assertFalse(result["passed"])
        self.assertIn("fingerprint", result["failure"])
        result = verifier.verify_tls("cetirun.example.com", minimum_valid_days=5000, now=time.time(), context_factory=lambda: self.context, connection_factory=self.connect)
        self.assertFalse(result["passed"])
        self.assertIn("validity", result["failure"])

    def test_invalid_inputs_and_tls_errors_are_not_hidden(self):
        with self.assertRaises(ValueError):
            verifier.verify_tls("not-a-host")
        for days in (0, -1, True, float("nan"), float("inf")):
            with self.subTest(days=days), self.assertRaises(ValueError):
                verifier.verify_tls("cetirun.example.com", minimum_valid_days=days)
        with self.assertRaises(ValueError):
            verifier.verify_tls("cetirun.example.com", expected_sha256="bad")
        self.context.wrap_socket.side_effect = ssl.SSLCertVerificationError("hostname mismatch")
        with self.assertRaises(ssl.SSLError):
            verifier.verify_tls("cetirun.example.com", context_factory=lambda: self.context, connection_factory=self.connect)


if __name__ == "__main__":
    unittest.main()
