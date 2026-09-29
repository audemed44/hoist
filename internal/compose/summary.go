package compose

import (
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Summary writes a Conventional Commits message for a compose file edit,
// scoped to the stack: "feat(main-stack): add romm" when a service is added,
// otherwise a chore, such as "chore(main-stack): bump shelfloom 0.4 → 0.5".
func Summary(stack string, before, after []byte) string {
	fallback := "chore(" + stack + "): update compose file"
	oldSvcs, errOld := services(before)
	newSvcs, errNew := services(after)
	if errOld != nil || errNew != nil {
		return fallback
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
			parts = append(parts, "bump "+name+" "+a+" → "+b)
		} else {
			parts = append(parts, "update "+name)
		}
	}
	if len(parts) == 0 {
		return fallback
	}
	kind := "chore"
	if strings.HasPrefix(parts[0], "add ") {
		kind = "feat"
	}
	prefix := kind + "(" + stack + "): "
	if len(parts) > 4 {
		return fmt.Sprintf("%s%s and %d more", prefix, strings.Join(parts[:3], ", "), len(parts)-3)
	}
	return prefix + strings.Join(parts, ", ")
}

var conventionalRe = regexp.MustCompile(
	`^(feat|fix|chore|docs|refactor|perf|test|build|ci|style|revert)(\([a-z0-9._/-]+\))?!?: \S`)

// Conventional reports whether a commit message's first line follows
// Conventional Commits: "<type>(<scope>): <summary>".
func Conventional(message string) bool {
	first, _, _ := strings.Cut(message, "\n")
	return conventionalRe.MatchString(first)
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
