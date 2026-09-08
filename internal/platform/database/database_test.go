package database

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMigrationChecksumsAreLineEndingIndependent(t *testing.T) {
	t.Parallel()

	lfCanonical, lfLegacy := migrationChecksums([]byte("SELECT 1;\nSELECT 2;\n"))
	crlfCanonical, crlfLegacy := migrationChecksums([]byte("SELECT 1;\r\nSELECT 2;\r\n"))

	if lfCanonical != crlfCanonical {
		t.Fatalf("canonical checksums differ: LF=%s CRLF=%s", lfCanonical, crlfCanonical)
	}
	if lfLegacy != crlfLegacy {
		t.Fatalf("legacy CRLF checksums differ: LF=%s CRLF=%s", lfLegacy, crlfLegacy)
	}
	if lfCanonical == lfLegacy {
		t.Fatal("canonical and legacy checksums should differ for multi-line SQL")
	}
}

func TestMigrationChecksumsStillDetectContentChanges(t *testing.T) {
	t.Parallel()

	original, _ := migrationChecksums([]byte("SELECT 1;\n"))
	changed, _ := migrationChecksums([]byte("SELECT 2;\n"))

	if original == changed {
		t.Fatal("different migration contents produced the same checksum")
	}
}

func TestAppliedDiscoveryMigrationRemainsImmutable(t *testing.T) {
	t.Parallel()

	contents, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "000003_local_discovery.up.sql"))
	if err != nil {
		t.Fatalf("read discovery migration: %v", err)
	}
	const appliedChecksum = "604ea8123af8d0a1a4ffd50a640dd24b41eff6d340f6cf20c6f1531d92f1c04e"
	if !matchesLegacyTrailingBlankLine(contents, appliedChecksum) {
		t.Fatal("historical discovery migration checksum is no longer accepted")
	}
}

func TestLegacyTrailingBlankLineDoesNotHideSQLChanges(t *testing.T) {
	t.Parallel()

	original := []byte("SELECT 1;\n")
	legacy := checksum([]byte("SELECT 1;\n\n"))
	if !matchesLegacyTrailingBlankLine(original, legacy) {
		t.Fatal("expected one historical trailing blank line to be accepted")
	}
	if matchesLegacyTrailingBlankLine([]byte("SELECT 2;\n"), legacy) {
		t.Fatal("SQL content change was accepted as a formatting variant")
	}
}
