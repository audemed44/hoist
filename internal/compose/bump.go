package compose

import (
	"fmt"
	"regexp"
	"strings"
)

// WithTag replaces the tag of an image reference ("ghcr.io/a/b:1.2" →
// "ghcr.io/a/b:1.3"), keeping the name as written.
func WithTag(ref, tag string) string {
	name := ref
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		name = ref[:i]
	}
	return name + ":" + tag
}

// BumpImage rewrites the one `image:` line naming oldRef to use newTag. It
// refuses when the image isn't written literally (a ${VAR} in .env sets it)
// or appears on more than one line, rather than guess.
func BumpImage(content []byte, oldRef, newTag string) ([]byte, string, error) {
	newRef := WithTag(oldRef, newTag)
	re := regexp.MustCompile(`(?m)^(\s*image:\s*["']?)` + regexp.QuoteMeta(oldRef) + `(["']?\s*(?:#.*)?)$`)
	matches := re.FindAllIndex(content, -1)
	switch len(matches) {
	case 0:
		return nil, "", fmt.Errorf("%s isn't written out in the compose file (is the tag set by a variable?); change it by hand", oldRef)
	case 1:
	default:
		return nil, "", fmt.Errorf("%s appears %d times in the compose file; change it by hand", oldRef, len(matches))
	}
	out := re.ReplaceAll(content, []byte("${1}"+strings.ReplaceAll(newRef, "$", "$$")+"${2}"))
	return out, newRef, nil
}
