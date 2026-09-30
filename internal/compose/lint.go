package compose

import (
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Hint is advice about a compose file. Hints never stop a save.
type Hint struct {
	Service string `json:"service"`
	Kind    string `json:"kind"` // latest | restart | healthcheck | secret
	Message string `json:"message"`
	// New is set when the saved file doesn't have this problem.
	New bool `json:"new"`
}

// LintInput is what Lint needs besides the file.
type LintInput struct {
	// Raw is the file as written, before interpolation, for spotting
	// secrets typed into it.
	Raw []byte
	// Services are the resolved services.
	Services []Service
	// Own reports whether an image is one of yours, which may stay on
	// :latest.
	Own func(image string) bool
	// ImageHealthcheck reports whether an image defines a healthcheck
	// itself; known is false when that can't be told (not pulled yet).
	ImageHealthcheck func(image string) (has, known bool)
}

var secretKey = regexp.MustCompile(`(?i)(^|_)(PASSWORD|PASSWD|PASS|SECRET|TOKEN|API_?KEY|PRIVATE_?KEY|ACCESS_?KEY|CREDENTIALS?)($|_)`)

// Lint checks each service for a pinned image (unless it's yours), a
// restart policy, a healthcheck (in the file or the image), and secrets
// written into the file instead of .env. Values are never repeated in hints.
func Lint(in LintInput) []Hint {
	out := []Hint{}
	for _, s := range in.Services {
		if s.Image != "" && !s.Lint.Build && isLatest(s.Image) && (in.Own == nil || !in.Own(s.Image)) {
			out = append(out, Hint{Service: s.Name, Kind: "latest",
				Message: s.Image + " follows :latest, so any deploy can bring a new version; pin a version tag"})
		}
		if s.Lint.Restart == "" || s.Lint.Restart == "no" {
			out = append(out, Hint{Service: s.Name, Kind: "restart",
				Message: "no restart policy, so it stays down after a crash or a reboot; add restart: unless-stopped"})
		}
		if !s.Lint.Healthcheck && !s.Lint.HealthcheckOff {
			has, known := false, false
			if in.ImageHealthcheck != nil && s.Image != "" {
				has, known = in.ImageHealthcheck(s.Image)
			}
			if !has {
				msg := "no healthcheck, so docker can't tell when it's up but not working"
				if !known {
					msg = "no healthcheck in the file (the image may have one; it isn't pulled yet)"
				}
				out = append(out, Hint{Service: s.Name, Kind: "healthcheck", Message: msg})
			}
		}
	}
	for _, k := range inlineSecrets(in.Raw) {
		out = append(out, Hint{Service: k[0], Kind: "secret",
			Message: k[1] + " is written into the compose file, so it's in git; move it to .env and use ${" + k[1] + "}"})
	}
	return out
}

func isLatest(image string) bool {
	if strings.Contains(image, "@") {
		return false
	}
	name := image[strings.LastIndex(image, "/")+1:]
	_, tag, ok := strings.Cut(name, ":")
	return !ok || tag == "latest"
}

// inlineSecrets finds environment entries whose name looks secret and whose
// value is typed in rather than a ${VARIABLE}: pairs of service and key.
func inlineSecrets(raw []byte) [][2]string {
	var doc struct {
		Services map[string]struct {
			Environment yaml.Node `yaml:"environment"`
		} `yaml:"services"`
	}
	if yaml.Unmarshal(raw, &doc) != nil {
		return nil
	}
	var out [][2]string
	check := func(service, key, value string) {
		value = strings.TrimSpace(value)
		if value == "" || strings.Contains(value, "${") || strings.HasPrefix(value, "$") ||
			strings.HasSuffix(strings.ToUpper(key), "_FILE") || !secretKey.MatchString(key) {
			return
		}
		out = append(out, [2]string{service, key})
	}
	for name, svc := range doc.Services {
		env := svc.Environment
		switch env.Kind {
		case yaml.MappingNode:
			for i := 0; i+1 < len(env.Content); i += 2 {
				check(name, env.Content[i].Value, env.Content[i+1].Value)
			}
		case yaml.SequenceNode:
			for _, item := range env.Content {
				if k, v, ok := strings.Cut(item.Value, "="); ok {
					check(name, k, v)
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i][0] != out[j][0] {
			return out[i][0] < out[j][0]
		}
		return out[i][1] < out[j][1]
	})
	return out
}

// OwnImages returns a test for images published by a GitHub owner: on ghcr.io
// or under the same name on Docker Hub.
func OwnImages(owner string) func(image string) bool {
	owner = strings.ToLower(owner)
	if owner == "" {
		return func(string) bool { return false }
	}
	prefixes := []string{"ghcr.io/" + owner + "/", owner + "/", "docker.io/" + owner + "/"}
	return func(image string) bool {
		image = strings.ToLower(image)
		for _, p := range prefixes {
			if strings.HasPrefix(image, p) {
				return true
			}
		}
		return false
	}
}
