// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxadmission

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type servingCertificate struct {
	ca          []byte
	certificate []byte
	key         []byte
	serial      *big.Int
}

func newServingCertificate(t *testing.T, serial int64) servingCertificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	root := &x509.Certificate{
		SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "CetiRun local test CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	ca, err := x509.CreateCertificate(rand.Reader, root, root, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{
		SerialNumber: big.NewInt(serial + 100), Subject: pkix.Name{CommonName: "localhost"},
		DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	certificate, err := x509.CreateCertificate(rand.Reader, leaf, root, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	encodedKey, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return servingCertificate{
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encodedKey}), leaf.SerialNumber,
	}
}

func (certificate servingCertificate) write(t *testing.T, directory string) {
	t.Helper()
	for name, content := range map[string][]byte{"tls.crt": certificate.certificate, "tls.key": certificate.key} {
		temporary := filepath.Join(directory, name+".next")
		if err := os.WriteFile(temporary, content, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(temporary, filepath.Join(directory, name)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestServingCertificateRequiresValidPlatformMaterial(t *testing.T) {
	directory := t.TempDir()
	now := time.Now()
	if err := checkServingCertificate(directory, now); err == nil {
		t.Fatal("missing certificates must not cause an implicit self-signed fallback")
	}
	certificate := newServingCertificate(t, 1)
	certificate.write(t, directory)
	if err := checkServingCertificate(directory, now); err != nil {
		t.Fatal(err)
	}
	for _, invalidTime := range []time.Time{now.Add(-2 * time.Hour), now.Add(2 * time.Hour)} {
		if err := checkServingCertificate(directory, invalidTime); err == nil {
			t.Fatal("not-yet-valid/expired serving material was accepted")
		}
	}
	if err := os.WriteFile(filepath.Join(directory, "tls.key"), []byte("not a private key"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkServingCertificate(directory, now); err == nil {
		t.Fatal("invalid serving material was accepted")
	}
}
