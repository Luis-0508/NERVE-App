package ntopng

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAutomaticInterfaceSelection(t *testing.T) {
	for _, tc := range []struct {
		name, interfaces string
		want             error
	}{
		{"single", `{"rc":0,"rsp":[{"ifid":7,"ifname":"enp3s0"}]}`, nil},
		{"multiple", `{"rc":0,"rsp":[{"ifid":7,"ifname":"enp3s0"},{"ifid":8,"ifname":"enp4s0"}]}`, ErrInvalidConfiguration},
		{"empty", `{"rc":0,"rsp":[]}`, ErrInvalidConfiguration},
		{"missing-id", `{"rc":0,"rsp":[{"ifname":"enp3s0"}]}`, ErrInvalidResponse},
		{"invalid", `{"rc":0,"rsp":{}}`, ErrInvalidResponse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				user, pass, ok := r.BasicAuth()
				if !ok || user != "reader" || pass != "test-only" {
					t.Error("credentials missing")
					w.WriteHeader(401)
					return
				}
				if r.URL.Path == "/prefix/lua/rest/v2/get/ntopng/interfaces.lua" {
					fmt.Fprint(w, tc.interfaces)
					return
				}
				if r.URL.Query().Get("ifid") != "7" {
					t.Error("resolved interface not used")
				}
				fmt.Fprint(w, `{"rc":0,"rsp":{"data":[],"totalRows":0}}`)
			}))
			defer srv.Close()
			source := New(Config{URL: srv.URL + "/prefix", Username: "reader", Password: "test-only", InterfaceID: "auto"})
			snapshot, err := source.Live(context.Background())
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if err == nil && (snapshot.Capture == nil || snapshot.Capture.InterfaceID != "7" || snapshot.Capture.InterfaceName != "enp3s0") {
				t.Fatal("missing actual interface")
			}
		})
	}
}
