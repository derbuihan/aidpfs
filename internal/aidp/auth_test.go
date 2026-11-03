package aidp

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAPIKeySignerUsesConfiguredProfile(t *testing.T) {
	dir := t.TempDir()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, "unit-test-key.pem")
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err = os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "oci-config")
	config := fmt.Sprintf(`[UNIT_TEST]
user=ocid1.user.oc1..synthetic
fingerprint=00:11:22:33:44:55:66:77
tenancy=ocid1.tenancy.oc1..synthetic
region=ap-osaka-1
key_file=%s
`, keyPath)
	if err = os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}

	request, err := http.NewRequest(http.MethodGet, "https://aidp.example.test/objects?path=%2FWorkspace", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Date", "Tue, 03 Nov 2026 00:00:00 GMT")
	if err = APIKeySigner(configPath, "UNIT_TEST").Sign(request); err != nil {
		t.Fatalf("sign with selected profile: %v", err)
	}
	authorization := request.Header.Get("Authorization")
	for _, want := range []string{
		`keyId="ocid1.tenancy.oc1..synthetic/ocid1.user.oc1..synthetic/00:11:22:33:44:55:66:77"`,
		`headers="date (request-target) host"`,
		`algorithm="rsa-sha256"`,
		`signature="`,
	} {
		if !strings.Contains(authorization, want) {
			t.Errorf("Authorization header missing %q", want)
		}
	}
	if got := request.Header.Get("Date"); got != "Tue, 03 Nov 2026 00:00:00 GMT" {
		t.Fatalf("signer changed caller-provided date header: %q", got)
	}
}

func TestAPIKeySignerReportsMissingProfile(t *testing.T) {
	request, err := http.NewRequest(http.MethodGet, "https://aidp.example.test/objects", nil)
	if err != nil {
		t.Fatal(err)
	}
	signer := APIKeySigner(filepath.Join(t.TempDir(), "missing-config"), "MISSING")
	if err = signer.Sign(request); err == nil {
		t.Fatal("signer succeeded without a configuration profile")
	}
}
