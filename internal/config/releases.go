package config

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Releases configures the release board: your own apps' pull requests,
// CI, image builds and deploys, read from GitHub with HOIST_GITHUB_TOKEN.
type Releases struct {
	// Owners whose repositories count as yours; default: the token's user.
	Owners []string `yaml:"owners,omitempty" json:"owners"`
	// Ignore leaves these repositories (owner/name) off the board.
	Ignore []string `yaml:"ignore,omitempty" json:"ignore"`
	// Workflow is the workflow file that builds and pushes the image.
	// Default docker.yml.
	Workflow string `yaml:"workflow,omitempty" json:"workflow"`
	// Every, if set (e.g. 10m), checks the board in the background and
	// notifies about changes: CI failing on a pull request, an image
	// waiting to be deployed. Off by default: the board is read when it's
	// looked at.
	Every string `yaml:"every,omitempty" json:"every"`
	// Notify is an Apprise API URL for the board's notifications; default
	// updates.notify.
	Notify string `yaml:"notify,omitempty" json:"-"`

	interval time.Duration
}

// Interval is the background check period; 0 means none.
func (r Releases) Interval() time.Duration { return r.interval }

var repoRe = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`)

func (r *Releases) normalise() error {
	if r.Workflow == "" {
		r.Workflow = "docker.yml"
	}
	if strings.Contains(r.Workflow, "/") {
		r.Workflow = r.Workflow[strings.LastIndex(r.Workflow, "/")+1:]
	}
	for _, repo := range r.Ignore {
		if !repoRe.MatchString(repo) {
			return fmt.Errorf("releases.ignore: %q should be owner/name", repo)
		}
	}
	switch r.Every {
	case "", "off", "0":
		r.interval = 0
	default:
		d, err := time.ParseDuration(r.Every)
		if err != nil || d < 2*time.Minute {
			return fmt.Errorf("releases.every: %q should be like 10m (at least 2m), or off", r.Every)
		}
		r.interval = d
	}
	return nil
}
