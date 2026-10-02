package deploy

import (
	"reflect"
	"testing"

	"github.com/audemed44/hoist/internal/docker"
	"github.com/audemed44/hoist/internal/jobs"
)

func TestDiff(t *testing.T) {
	before := []docker.Container{
		{ID: "1", Name: "same", Service: "same", ImageID: "i1", State: "running"},
		{ID: "2", Name: "config", Service: "config", ImageID: "i2", State: "running"},
		{ID: "3", Name: "image", Service: "image", ImageID: "i3", State: "running"},
		{ID: "4", Name: "stopped", Service: "stopped", ImageID: "i4", State: "exited"},
		{ID: "5", Name: "gone", Service: "gone", ImageID: "i5", State: "running"},
		{ID: "6", Name: "run-1", Service: "same", OneOff: true},
	}
	after := []docker.Container{
		{ID: "1", Name: "same", Service: "same", ImageID: "i1", State: "running"},
		{ID: "2b", Name: "config", Service: "config", ImageID: "i2", State: "running"},
		{ID: "3b", Name: "image", Service: "image", ImageID: "i3b", State: "running"},
		{ID: "4", Name: "stopped", Service: "stopped", ImageID: "i4", State: "running"},
		{ID: "7", Name: "new", Service: "new", ImageID: "i7", State: "running"},
	}
	got := Diff(before, after)
	want := &jobs.Result{
		Created:   []string{"new"},
		Recreated: []string{"config", "image"},
		Removed:   []string{"gone"},
		Started:   []string{"stopped"},
		Updated:   []string{"image"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("diff = %+v", got)
	}
	if s := got.Summary(); s != "Created new; recreated config, image; started stopped; removed gone" {
		t.Errorf("summary = %q", s)
	}
	if s := Diff(before, before).Summary(); s != "Nothing changed" {
		t.Errorf("empty summary = %q", s)
	}
}

func TestBackToSleep(t *testing.T) {
	before := []docker.Container{
		{Name: "convertx", Service: "convertx", State: "exited"},
		{Name: "bentopdf", Service: "bentopdf", State: "exited"},
		{Name: "it-tools", Service: "it-tools", State: "exited"},
	}
	res := &jobs.Result{Started: []string{"convertx", "it-tools"}, Recreated: []string{"bentopdf"}}
	// it-tools was stopped by hand, not by Gatehouse; bentopdf was recreated.
	got := backToSleep(res, before, []string{"convertx", "bentopdf"})
	if !reflect.DeepEqual(got, []string{"convertx"}) {
		t.Fatalf("got %v", got)
	}
}
