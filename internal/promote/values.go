package promote

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"
)

// PatchImage sets the top-level `image` scalar and leaves every other key as-is.
func PatchImage(in []byte, imageRef string) ([]byte, error) {
	if len(bytes.TrimSpace(in)) == 0 {
		return nil, fmt.Errorf("values file is empty")
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(in, &doc); err != nil {
		return nil, fmt.Errorf("parse values: %w", err)
	}
	if err := setImage(&doc, imageRef); err != nil {
		return nil, err
	}

	out, err := encodeYAML(&doc)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func alreadyPinned(in []byte, imageRef string) bool {
	var m map[string]any
	if err := yaml.Unmarshal(in, &m); err != nil {
		return false
	}
	s, _ := m["image"].(string)
	return s == imageRef
}

func encodeYAML(doc *yaml.Node) ([]byte, error) {
	node := doc
	if doc.Kind == yaml.DocumentNode {
		if len(doc.Content) == 0 {
			return nil, fmt.Errorf("values document is empty")
		}
		node = doc.Content[0]
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(node); err != nil {
		return nil, fmt.Errorf("encode values: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func setImage(node *yaml.Node, imageRef string) error {
	if node == nil {
		return fmt.Errorf("values document is empty")
	}
	if node.Kind == yaml.DocumentNode {
		if len(node.Content) == 0 {
			return fmt.Errorf("values document is empty")
		}
		return setImage(node.Content[0], imageRef)
	}
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("values must be a YAML mapping")
	}

	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i]
		if key.Value != "image" {
			continue
		}
		val := node.Content[i+1]
		val.Kind = yaml.ScalarNode
		val.Tag = "!!str"
		val.Value = imageRef
		val.Style = yaml.DoubleQuotedStyle
		val.Content = nil
		return nil
	}

	node.Content = append(node.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "image"},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: imageRef, Style: yaml.DoubleQuotedStyle},
	)
	return nil
}
