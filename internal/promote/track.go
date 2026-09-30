package promote

import (
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

var trackSHARe = regexp.MustCompile(`^[a-f0-9]{7,40}$`)

// Track is the desired production commit. image remains the realized digest.
type Track struct {
	Follow bool
	SHA    string
}

func (t Track) FollowsMain() bool {
	return t.Follow || t.SHA == ""
}

func (t Track) Short() string {
	if t.FollowsMain() {
		return "main"
	}
	if len(t.SHA) > 12 {
		return t.SHA[:12]
	}
	return t.SHA
}

func (t Track) MatchesCommit(sha string) bool {
	if t.FollowsMain() {
		return true
	}
	sha = strings.ToLower(strings.TrimSpace(sha))
	pin := t.SHA
	if sha == "" || pin == "" {
		return false
	}
	return sha == pin || strings.HasPrefix(sha, pin) || strings.HasPrefix(pin, sha)
}

// ParseTrack reads the top-level `track` key. Missing, empty, main, and
// latest all mean "follow the production branch".
func ParseTrack(in []byte) (Track, error) {
	v, err := ParseValuesMeta(in)
	return v.Track, err
}

type ValuesMeta struct {
	Track Track
	Image string
}

func ParseValuesMeta(in []byte) (ValuesMeta, error) {
	var m map[string]any
	if err := yaml.Unmarshal(in, &m); err != nil {
		return ValuesMeta{}, fmt.Errorf("parse values: %w", err)
	}
	raw, _ := m["track"].(string)
	track, err := parseTrackValue(raw)
	if err != nil {
		return ValuesMeta{}, err
	}
	image, _ := m["image"].(string)
	return ValuesMeta{Track: track, Image: strings.TrimSpace(image)}, nil
}

func parseTrackValue(raw string) (Track, error) {
	raw = strings.ToLower(strings.TrimSpace(raw))
	raw = strings.TrimPrefix(raw, ":")
	if raw == "" || raw == "main" || raw == "latest" {
		return Track{Follow: true}, nil
	}
	if !trackSHARe.MatchString(raw) {
		return Track{}, fmt.Errorf("track %q must be main, latest, empty, or a 7-40 hex commit SHA", raw)
	}
	return Track{SHA: raw}, nil
}
