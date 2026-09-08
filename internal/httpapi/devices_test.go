package httpapi

import (
	"net/http/httptest"
	"testing"
)

func TestIsUUID(t *testing.T) {
	if !isUUID("01991b36-0478-7de8-9daa-22b30c4a9255") {
		t.Fatal("isUUID() rejected a valid UUIDv7")
	}
	if isUUID("not-a-device") {
		t.Fatal("isUUID() accepted an invalid identifier")
	}
}

func TestIntegerQuery(t *testing.T) {
	request := httptest.NewRequest("GET", "/api/v1/devices?limit=75", nil)
	value, err := integerQuery(request, "limit", 50)
	if err != nil || value != 75 {
		t.Fatalf("integerQuery() = %d, %v", value, err)
	}

	request = httptest.NewRequest("GET", "/api/v1/devices?limit=all", nil)
	if _, err := integerQuery(request, "limit", 50); err == nil {
		t.Fatal("integerQuery() accepted a non-integer value")
	}
}
