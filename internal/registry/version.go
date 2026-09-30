package registry

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Version is a tag that looks like a version: an optional "v", one to four
// numbers, and an optional suffix whose trailing number counts as a build
// ("-ls123" is suffix "-ls", build 123; "-alpine" has no build).
type Version struct {
	Tag    string
	Prefix string
	Nums   []int
	Suffix string
	Build  int // -1 without one
}

var versionRe = regexp.MustCompile(`^(v?)(\d+(?:\.\d+){0,3})([-_.+][A-Za-z0-9._-]*)?$`)
var trailingNum = regexp.MustCompile(`^(.*?)(\d+)$`)

// ParseVersion reads a tag as a version; ok is false for tags like
// "latest" or "stable".
func ParseVersion(tag string) (Version, bool) {
	m := versionRe.FindStringSubmatch(tag)
	if m == nil {
		return Version{}, false
	}
	v := Version{Tag: tag, Prefix: m[1], Suffix: m[3], Build: -1}
	for _, part := range strings.Split(m[2], ".") {
		n, err := strconv.Atoi(part)
		if err != nil {
			return Version{}, false
		}
		v.Nums = append(v.Nums, n)
	}
	if sm := trailingNum.FindStringSubmatch(v.Suffix); sm != nil {
		v.Suffix = sm[1]
		v.Build, _ = strconv.Atoi(sm[2])
	}
	return v, true
}

// sameShape reports whether two versions can be compared: same prefix,
// same number of parts, same suffix. It keeps "1.2.3" away from
// "1.3.0-rc1" and "2-alpine" away from "2".
func (v Version) sameShape(o Version) bool {
	return v.Prefix == o.Prefix && len(v.Nums) == len(o.Nums) && v.Suffix == o.Suffix &&
		(v.Build < 0) == (o.Build < 0)
}

// compare orders same-shaped versions.
func (v Version) compare(o Version) int {
	for i := range v.Nums {
		if v.Nums[i] != o.Nums[i] {
			if v.Nums[i] < o.Nums[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case v.Build < o.Build:
		return -1
	case v.Build > o.Build:
		return 1
	}
	return 0
}

// Bump is how far a newer version moves: "major", "minor" or "patch" (a
// build-only change counts as a patch).
func (v Version) Bump(newer Version) string {
	switch {
	case newer.Nums[0] != v.Nums[0]:
		return "major"
	case len(v.Nums) > 1 && newer.Nums[1] != v.Nums[1]:
		return "minor"
	}
	return "patch"
}

// Newer returns the same-shaped versions among tags that are newer than
// current, oldest first.
func Newer(current Version, tags []string) []Version {
	var out []Version
	for _, t := range tags {
		v, ok := ParseVersion(t)
		if ok && v.sameShape(current) && v.compare(current) > 0 {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].compare(out[j]) < 0 })
	return out
}

// Allows reports whether an update policy covers a bump: "patch" allows
// patches, "minor" also minors. Majors are never automatic.
func Allows(policy, bump string) bool {
	switch policy {
	case "minor":
		return bump == "minor" || bump == "patch"
	case "patch":
		return bump == "patch"
	}
	return false
}

// Older returns the same-shaped versions among tags that are older than
// current, newest first.
func Older(current Version, tags []string) []Version {
	var out []Version
	for _, t := range tags {
		v, ok := ParseVersion(t)
		if ok && v.sameShape(current) && v.compare(current) < 0 {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].compare(out[j]) > 0 })
	return out
}
