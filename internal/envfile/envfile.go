// Package envfile reads and edits a compose .env file without disturbing its
// comments, blank lines or key order.
package envfile

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var keyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*$`)

type line struct {
	raw   string // the line as written, for comments and blanks
	key   string // empty for comments and blanks
	value string // everything after the first '=', untouched
}

type File struct {
	lines []line
}

// Parse reads .env content. Lines that aren't KEY=value are kept verbatim.
func Parse(data []byte) *File {
	f := &File{}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return f
	}
	for _, raw := range strings.Split(text, "\n") {
		l := line{raw: raw}
		trimmed := strings.TrimSpace(raw)
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			k, v, ok := strings.Cut(strings.TrimPrefix(trimmed, "export "), "=")
			if ok && keyRe.MatchString(strings.TrimSpace(k)) {
				l.key, l.value = strings.TrimSpace(k), v
			}
		}
		f.lines = append(f.lines, l)
	}
	return f
}

// Read parses the file at path; a missing file is an empty one.
func Read(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &File{}, nil
	}
	if err != nil {
		return nil, err
	}
	return Parse(data), nil
}

// Keys returns the keys in file order.
func (f *File) Keys() []string {
	var keys []string
	for _, l := range f.lines {
		if l.key != "" {
			keys = append(keys, l.key)
		}
	}
	return keys
}

// Get returns a key's raw value (quotes included, as written).
func (f *File) Get(key string) (string, bool) {
	for i := len(f.lines) - 1; i >= 0; i-- { // the last one wins, as in compose
		if f.lines[i].key == key {
			return f.lines[i].value, true
		}
	}
	return "", false
}

// Change is one entry of an edit. A nil Value keeps the current value.
type Change struct {
	Key   string  `json:"key"`
	Value *string `json:"value"`
}

// Apply replaces the file's keys with changes: listed keys keep their place
// (and value, when Value is nil), missing ones are dropped, new ones are
// appended. Comments and blank lines stay where they were.
func (f *File) Apply(changes []Change) error {
	want := map[string]*string{}
	var order []string
	for _, c := range changes {
		key := strings.TrimSpace(c.Key)
		if !keyRe.MatchString(key) {
			return fmt.Errorf("%q is not a valid variable name", c.Key)
		}
		if _, dup := want[key]; dup {
			return fmt.Errorf("%s is listed twice", key)
		}
		if c.Value != nil && strings.ContainsAny(*c.Value, "\r\n") {
			return fmt.Errorf("%s: values can't span lines", key)
		}
		if c.Value == nil {
			if _, ok := f.Get(key); !ok {
				return fmt.Errorf("%s has no value to keep", key)
			}
		}
		want[key] = c.Value
		order = append(order, key)
	}
	var out []line
	placed := map[string]bool{}
	for _, l := range f.lines {
		if l.key == "" {
			out = append(out, l)
			continue
		}
		v, ok := want[l.key]
		if !ok || placed[l.key] {
			continue // removed, or a duplicate of a key already written
		}
		placed[l.key] = true
		if v != nil {
			l.value = *v
			l.raw = l.key + "=" + *v
		} else {
			// Keep the last value compose would have used, on this line.
			l.value, _ = f.Get(l.key)
			l.raw = l.key + "=" + l.value
		}
		out = append(out, l)
	}
	for _, key := range order {
		if !placed[key] {
			out = append(out, line{key: key, value: *want[key], raw: key + "=" + *want[key]})
		}
	}
	f.lines = out
	return nil
}

func (f *File) Bytes() []byte {
	var b bytes.Buffer
	for _, l := range f.lines {
		b.WriteString(l.raw)
		b.WriteByte('\n')
	}
	return b.Bytes()
}

// Write replaces path atomically, keeping its permissions (0600 for a new
// file, since it holds secrets).
func (f *File) Write(path string) error {
	return WriteAtomic(path, f.Bytes(), 0o600)
}

// WriteAtomic writes via a temp file in the same folder and renames it over
// path, keeping the existing file's mode if there is one.
func WriteAtomic(path string, data []byte, mode os.FileMode) error {
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".hoist-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

var refRe = regexp.MustCompile(`\$(\$|\{([A-Za-z_][A-Za-z0-9_]*)([^}]*)\}|([A-Za-z_][A-Za-z0-9_]*))`)

// Refs lists the variables a compose file interpolates. Required are the
// ones used without a default (${X:-d} and ${X-d} have one).
func Refs(compose []byte) (all, required []string) {
	seen := map[string]bool{}
	req := map[string]bool{}
	for _, m := range refRe.FindAllSubmatch(compose, -1) {
		if string(m[1]) == "$" {
			continue // $$ is an escaped dollar
		}
		name, rest := string(m[2]), string(m[3])
		if name == "" {
			name = string(m[4])
		}
		seen[name] = true
		if !strings.HasPrefix(rest, ":-") && !strings.HasPrefix(rest, "-") {
			req[name] = true
		}
	}
	for k := range seen {
		all = append(all, k)
	}
	for k := range req {
		required = append(required, k)
	}
	sort.Strings(all)
	sort.Strings(required)
	return all, required
}
