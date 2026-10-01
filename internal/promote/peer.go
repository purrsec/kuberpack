package promote

import (
	"fmt"
	"strings"
)

// NetworkPeer is an allowlisted destination. Kind is "app" or "postgres".
type NetworkPeer struct {
	Kind string
	Name string
}

func ParsePeer(raw string) (NetworkPeer, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return NetworkPeer{}, fmt.Errorf("peer is empty")
	}
	kind, name := "app", raw
	if kindName, rest, ok := strings.Cut(raw, ":"); ok {
		kind, name = strings.TrimSpace(kindName), strings.TrimSpace(rest)
	}
	name = sanitizeName(name)
	if name == "" {
		return NetworkPeer{}, fmt.Errorf("peer name is invalid")
	}
	switch kind {
	case "app":
		return NetworkPeer{Kind: "app", Name: name}, nil
	case "postgres":
		return NetworkPeer{Kind: "postgres", Name: name}, nil
	default:
		return NetworkPeer{}, fmt.Errorf("peer kind %q is not supported", kind)
	}
}

func ParsePeers(raw []string) ([]NetworkPeer, error) {
	out := make([]NetworkPeer, 0, len(raw))
	seen := map[string]bool{}
	for _, r := range raw {
		p, err := ParsePeer(r)
		if err != nil {
			return nil, err
		}
		key := p.Kind + ":" + p.Name
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, p)
	}
	return out, nil
}

func FormatPeers(peers []NetworkPeer) []string {
	out := make([]string, 0, len(peers))
	for _, p := range peers {
		if p.Kind == "app" {
			out = append(out, p.Name)
			continue
		}
		out = append(out, p.Kind+":"+p.Name)
	}
	return out
}
