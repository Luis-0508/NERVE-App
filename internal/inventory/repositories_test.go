package inventory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	pgxmock "github.com/pashagolub/pgxmock/v4"
)

func TestNormalizeListParams(t *testing.T) {
	params, err := NormalizeListParams(DeviceListParams{Search: "  edge  "})
	if err != nil {
		t.Fatalf("NormalizeListParams() error = %v", err)
	}
	if params.Search != "edge" || params.Sort != "name" || params.Direction != "asc" || params.Limit != 50 {
		t.Fatalf("NormalizeListParams() = %#v", params)
	}

	if _, err := NormalizeListParams(DeviceListParams{Direction: "sideways"}); err == nil {
		t.Fatal("NormalizeListParams() accepted an invalid direction")
	}
	if _, err := NormalizeListParams(DeviceListParams{Limit: 201}); err == nil {
		t.Fatal("NormalizeListParams() accepted a limit above 200")
	}
}

func TestDeviceRepositoryList(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool() error = %v", err)
	}
	defer mock.Close()

	seen := time.Date(2026, time.September, 4, 9, 30, 0, 0, time.UTC)
	mock.ExpectQuery("SELECT e.id::text, e.name, e.status").
		WithArgs("edge", "online", "network", 25, 0).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "name", "status", "classification", "hostname", "ip", "mac",
			"manufacturer", "operating_system", "first_seen_at", "last_seen_at", "total",
		}).AddRow(
			"01991b36-0478-7de8-9daa-22b30c4a9255", "Edge gateway", "online", "network",
			"edge-01", "192.0.2.1", "00:11:22:33:44:55", "NERVE", "Linux", seen, seen, int64(1),
		))

	repository := NewRepositories(mock).Devices
	result, err := repository.List(context.Background(), DeviceListParams{
		Search: "edge", Status: "online", Classification: "network",
		Sort: "last_seen", Direction: "desc", Limit: 25,
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if result.Total != 1 || len(result.Items) != 1 || result.Items[0].PrimaryIPAddress != "192.0.2.1" {
		t.Fatalf("List() = %#v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet database expectation: %v", err)
	}
}

func TestDeviceRepositoryGetNotFound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool() error = %v", err)
	}
	defer mock.Close()

	id := "01991b36-0478-7de8-9daa-22b30c4a9255"
	mock.ExpectQuery("SELECT e.id::text, e.name, e.status").WithArgs(id).WillReturnError(pgx.ErrNoRows)

	_, err = NewRepositories(mock).Devices.Get(context.Background(), id)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get() error = %v, want ErrNotFound", err)
	}
}

func TestInterfaceRepositoryGroupsAddresses(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool() error = %v", err)
	}
	defer mock.Close()

	deviceID := "01991b36-0478-7de8-9daa-22b30c4a9255"
	interfaceID := "01991b36-a590-79ee-b0f4-0de282a39f06"
	seen := time.Date(2026, time.September, 4, 9, 30, 0, 0, time.UTC)
	columns := []string{"interface_id", "name", "mac", "media_type", "mtu", "speed", "is_up", "address_id", "address", "version", "is_primary", "valid_from", "valid_until"}
	rows := pgxmock.NewRows(columns).
		AddRow(interfaceID, "eth0", "00:11:22:33:44:55", "ethernet", int32(1500), int64(1_000_000_000), true, "01991b37-53fe-7bc6-bf65-718d6d97585b", "192.0.2.1", int16(4), true, seen, nil).
		AddRow(interfaceID, "eth0", "00:11:22:33:44:55", "ethernet", int32(1500), int64(1_000_000_000), true, "01991b37-bad8-7a14-bd09-11dab419191d", "2001:db8::1", int16(6), false, seen, nil)
	mock.ExpectQuery("SELECT ni.entity_id::text, ni.name").WithArgs(deviceID).WillReturnRows(rows)

	items, err := NewRepositories(mock).Interfaces.ListByDevice(context.Background(), deviceID)
	if err != nil {
		t.Fatalf("ListByDevice() error = %v", err)
	}
	if len(items) != 1 || len(items[0].Addresses) != 2 || items[0].MTU == nil || *items[0].MTU != 1500 {
		t.Fatalf("ListByDevice() = %#v", items)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet database expectation: %v", err)
	}
}
