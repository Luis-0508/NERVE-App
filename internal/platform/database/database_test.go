package database

import "testing"

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
