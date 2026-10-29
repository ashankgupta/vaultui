package ui

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"
)

func TestDescribe(t *testing.T) {
	if k, _, b := describe(`{"a":1}`); k != "JSON" || len(b) != 3 {
		t.Fatalf("json: %s %v", k, b)
	}
	// {"alg":"none"}.{"sub":"x","exp":1}.sig
	jwt := "eyJhbGciOiJub25lIn0.eyJzdWIiOiJ4IiwiZXhwIjoxfQ.sig"
	if k, d, _ := describe(jwt); k != "JWT" || len(d) != 1 || !strings.Contains(d[0], "EXPIRED") {
		t.Fatalf("jwt: %s %v", k, d)
	}
	if k, _, _ := describe("a\nb"); k != "multi-line" {
		t.Fatal(k)
	}
	if k, _, _ := describe("plain"); k != "text" {
		t.Fatal(k)
	}

	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "db.internal"},
		NotBefore: time.Now(), NotAfter: time.Now().Add(10 * 24 * time.Hour), DNSNames: []string{"db.internal"}}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	cert := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	if k, d, _ := describe(cert); k != "certificate" || !strings.Contains(strings.Join(d, " "), "db.internal") {
		t.Fatalf("cert: %s %v", k, d)
	}
}
