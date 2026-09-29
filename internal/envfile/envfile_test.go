package envfile

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func ptr(s string) *string { return &s }

const sample = `# Paths
TZ=Europe/London
export DB_PASSWORD="s3cret=="

# Keys
API_KEY=abc
not a variable
API_KEY=override
`

func TestParse(t *testing.T) {
	f := Parse([]byte(sample))
	if got := f.Keys(); !reflect.DeepEqual(got, []string{"TZ", "DB_PASSWORD", "API_KEY", "API_KEY"}) {
		t.Errorf("keys = %v", got)
	}
	if v, _ := f.Get("DB_PASSWORD"); v != `"s3cret=="` {
		t.Errorf("DB_PASSWORD = %q", v)
	}
	if v, _ := f.Get("API_KEY"); v != "override" {
		t.Errorf("API_KEY = %q, want the last one", v)
	}
	if string(f.Bytes()) != sample {
		t.Errorf("round trip changed the file:\n%s", f.Bytes())
	}
}

func TestApply(t *testing.T) {
	f := Parse([]byte(sample))
	err := f.Apply([]Change{
		{Key: "TZ", Value: ptr("UTC")},
		{Key: "API_KEY"}, // keep
		{Key: "NEW", Value: ptr("1")},
		// DB_PASSWORD dropped
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `# Paths
TZ=UTC

# Keys
API_KEY=override
not a variable
NEW=1
`
	if got := string(f.Bytes()); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestApplyErrors(t *testing.T) {
	for name, changes := range map[string][]Change{
		"bad key":   {{Key: "1ABC", Value: ptr("x")}},
		"duplicate": {{Key: "A", Value: ptr("x")}, {Key: "A", Value: ptr("y")}},
		"newline":   {{Key: "A", Value: ptr("x\ny")}},
		"keep new":  {{Key: "MISSING"}},
	} {
		if err := Parse([]byte(sample)).Apply(changes); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestWriteKeepsMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("A=1\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	f, _ := Read(path)
	_ = f.Apply([]Change{{Key: "A", Value: ptr("2")}})
	if err := f.Write(path); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	data, _ := os.ReadFile(path)
	if info.Mode().Perm() != 0o640 || string(data) != "A=2\n" {
		t.Errorf("mode %v, content %q", info.Mode().Perm(), data)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("temp file left behind: %v", entries)
	}
}

func TestReadMissing(t *testing.T) {
	f, err := Read(filepath.Join(t.TempDir(), ".env"))
	if err != nil || len(f.Keys()) != 0 {
		t.Errorf("f = %v, err = %v", f, err)
	}
}

func TestRefs(t *testing.T) {
	all, required := Refs([]byte(`
services:
  a:
    image: app:${TAG:-latest}
    environment:
      - TZ=${TZ}
      - KEY=$API_KEY
      - LITERAL=$$HOME
      - OPT=${OPT-}
      - REQ=${REQ:?set me}
`))
	if !reflect.DeepEqual(all, []string{"API_KEY", "OPT", "REQ", "TAG", "TZ"}) {
		t.Errorf("all = %v", all)
	}
	if !reflect.DeepEqual(required, []string{"API_KEY", "REQ", "TZ"}) {
		t.Errorf("required = %v", required)
	}
}
