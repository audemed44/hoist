package compose

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Summary writes a commit message for a compose file edit, such as
// "main-stack: shelfloom 0.4 → 0.5" or "main-stack: add romm, remove yamtrack".
func Summary(stack string, before, after []byte) string {
	oldSvcs, errOld := services(before)
	newSvcs, errNew := services(after)
	if errOld != nil || errNew != nil {
		return stack + ": update compose file"
	}
	var parts []string
	for _, name := range sortedKeys(newSvcs) {
		if _, ok := oldSvcs[name]; !ok {
			parts = append(parts, "add "+name)
		}
	}
	for _, name := range sortedKeys(oldSvcs) {
		if _, ok := newSvcs[name]; !ok {
			parts = append(parts, "remove "+name)
		}
	}
	for _, name := range sortedKeys(newSvcs) {
		old, ok := oldSvcs[name]
		if !ok || reflect.DeepEqual(old, newSvcs[name]) {
			continue
		}
		if a, b, only := imageOnly(old, newSvcs[name]); only {
			parts = append(parts, name+" "+a+" → "+b)
		} else {
			parts = append(parts, "update "+name)
		}
	}
	switch {
	case len(parts) == 0:
		return stack + ": update compose file"
	case len(parts) > 4:
		return fmt.Sprintf("%s: %s and %d more", stack, strings.Join(parts[:3], ", "), len(parts)-3)
	}
	return stack + ": " + strings.Join(parts, ", ")
}

func services(data []byte) (map[string]any, error) {
	var doc struct {
		Services map[string]any `yaml:"services"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if doc.Services == nil {
		doc.Services = map[string]any{}
	}
	return doc.Services, nil
}

// imageOnly reports whether only the image changed, and how to describe it:
// just the tags when the repository is the same.
func imageOnly(old, cur any) (string, string, bool) {
	o, ok1 := old.(map[string]any)
	n, ok2 := cur.(map[string]any)
	if !ok1 || !ok2 {
		return "", "", false
	}
	oi, _ := o["image"].(string)
	ni, _ := n["image"].(string)
	if oi == "" || ni == "" || oi == ni {
		return "", "", false
	}
	rest := func(m map[string]any) map[string]any {
		c := make(map[string]any, len(m))
		for k, v := range m {
			if k != "image" {
				c[k] = v
			}
		}
		return c
	}
	if !reflect.DeepEqual(rest(o), rest(n)) {
		return "", "", false
	}
	oRepo, oTag := splitImage(oi)
	nRepo, nTag := splitImage(ni)
	if oRepo == nRepo {
		return oTag, nTag, true
	}
	return oi, ni, true
}

// splitImage splits "ghcr.io/a/b:1.2" into "ghcr.io/a/b" and "1.2".
func splitImage(ref string) (string, string) {
	if i := strings.Index(ref, "@"); i >= 0 {
		return ref[:i], ref[i+1:]
	}
	slash := strings.LastIndex(ref, "/")
	if i := strings.LastIndex(ref, ":"); i > slash {
		return ref[:i], ref[i+1:]
	}
	return ref, "latest"
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
