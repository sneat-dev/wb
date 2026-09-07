package wbconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// SetRemoteHub updates only the remote provider fields owned by hub
// enrollment. Other top-level configuration and remote settings are retained.
func SetRemoteHub(path, hubURL, machine, tokenFile string) error {
	var document yaml.Node
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := yaml.Unmarshal(raw, &document); err != nil {
			return fmt.Errorf("parse config %s: %w", path, err)
		}
	case errors.Is(err, os.ErrNotExist):
		document = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}
	default:
		return fmt.Errorf("read config %s: %w", path, err)
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("parse config %s: top level must be a mapping", path)
	}
	root := document.Content[0]
	remote := mappingValue(root, "remote")
	if remote == nil {
		remote = &yaml.Node{Kind: yaml.MappingNode}
		root.Content = append(root.Content, scalar("remote"), remote)
	} else if remote.Kind != yaml.MappingNode {
		return fmt.Errorf("parse config %s: remote must be a mapping", path)
	}
	setMappingScalar(remote, "provider", "hub")
	setMappingScalar(remote, "url", hubURL)
	setMappingScalar(remote, "machine", machine)
	setMappingScalar(remote, "token_file", tokenFile)

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".wb-config-*.yaml")
	if err != nil {
		return fmt.Errorf("stage config: %w", err)
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("protect staged config: %w", err)
	}
	encoder := yaml.NewEncoder(temporary)
	encoder.SetIndent(2)
	if err := encoder.Encode(&document); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("encode config: %w", err)
	}
	if err := encoder.Close(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("finish config: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync config: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close config: %w", err)
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}

func mappingValue(mapping *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

func setMappingScalar(mapping *yaml.Node, key, value string) {
	if existing := mappingValue(mapping, key); existing != nil {
		existing.Kind = yaml.ScalarNode
		existing.Tag = "!!str"
		existing.Value = value
		existing.Content = nil
		return
	}
	mapping.Content = append(mapping.Content, scalar(key), scalar(value))
}

func scalar(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}
