package sleeping

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAsleep(t *testing.T) {
	calls, up := 0, true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if !up || r.Header.Get("Authorization") != "Bearer ro" {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Write([]byte(`{"hosts":[
			{"container":"convertx","idle_stop":"30m","state":"sleeping"},
			{"container":"bentopdf","idle_stop":"30m","state":"awake"},
			{"container":"shelfloom","state":""}]}`))
	}))
	defer srv.Close()
	c := New(srv.URL+"/", "ro")
	got := c.Asleep(context.Background())
	if len(got) != 1 || got["convertx"] != "sleeping" {
		t.Fatalf("got %v", got)
	}
	c.Asleep(context.Background())
	if calls != 1 {
		t.Fatalf("not cached: %d calls", calls)
	}
	// Gatehouse unreachable: the last answer stands.
	up = false
	c.at = c.at.Add(-cacheFor)
	if got := c.Asleep(context.Background()); got["convertx"] != "sleeping" {
		t.Fatalf("lost the last answer: %v", got)
	}
	var none *Client
	if none.Asleep(context.Background()) != nil {
		t.Fatal("a nil client knows of sleeping containers")
	}
}
