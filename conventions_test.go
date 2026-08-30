package otel_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	otel "github.com/bitsmithy/go-otel"
)

func TestPinnedTelemetryConventions(t *testing.T) {
	content, err := os.ReadFile("testdata/telemetry-conventions-v1.0.0.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		ContractVersion string `json:"contract_version"`
	}
	if err := json.Unmarshal(content, &manifest); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	checksum := hex.EncodeToString(digest[:])

	if got, want := []string{otel.ConventionsVersion, manifest.ContractVersion, checksum}, []string{"1.0.0", otel.ConventionsVersion, otel.ConventionsSHA256}; !equalStrings(got, want) {
		t.Fatalf("pinned contract = %v, want %v", got, want)
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
