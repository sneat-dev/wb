package wbconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sneat-dev/wb/internal/filewrite"
	"gopkg.in/yaml.v3"
)

// SetRemoteHub updates only the remote provider fields owned by hub
// enrollment. Other top-level configuration and remote settings are retained.
func SetRemoteHub(path, hubURL, machine, tokenFile string) error {
	return setRemoteHubInjected(path, hubURL, machine, tokenFile, nil)
}

// setRemoteHubInjected is SetRemoteHub's test seam (task-9 PR-8): every
// production call site reaches it only through SetRemoteHub, which always
// passes a nil *filewrite.Injector, so production behaviour is unchanged. A
// test passes its own Injector to reach the create/chmod/write/sync/close/
// rename failure branches deterministically.
func setRemoteHubInjected(path, hubURL, machine, tokenFile string, inj *filewrite.Injector) error {
	document, root, err := loadDocument(path)
	if err != nil {
		return err
	}
	remote, err := section(path, root, "remote")
	if err != nil {
		return err
	}
	setMappingScalar(remote, "provider", "hub")
	setMappingScalar(remote, "url", hubURL)
	setMappingScalar(remote, "machine", machine)
	setMappingScalar(remote, "token_file", tokenFile)
	return writeDocument(path, document, inj)
}

// loadDocument reads the configuration as a YAML node tree, or an empty
// mapping when the file does not exist, so a writer can change its own keys
// and keep every other setting, comment and order.
func loadDocument(path string) (*yaml.Node, *yaml.Node, error) {
	var document yaml.Node
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := yaml.Unmarshal(raw, &document); err != nil {
			return nil, nil, fmt.Errorf("parse config %s: %w", path, err)
		}
	case errors.Is(err, os.ErrNotExist):
		document = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}
	default:
		return nil, nil, fmt.Errorf("read config %s: %w", path, err)
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, nil, fmt.Errorf("parse config %s: top level must be a mapping", path)
	}
	return &document, document.Content[0], nil
}

// section is the top-level mapping named key, added when absent.
func section(path string, root *yaml.Node, key string) (*yaml.Node, error) {
	value := mappingValue(root, key)
	if value == nil {
		value = &yaml.Node{Kind: yaml.MappingNode}
		root.Content = append(root.Content, scalar(key), value)
	} else if value.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("parse config %s: %s must be a mapping", path, key)
	}
	return value, nil
}

// writeDocument replaces the configuration atomically with a 0600 file.
func writeDocument(path string, document *yaml.Node, inj *filewrite.Injector) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	temporary, err := filewrite.CreateTemp(filepath.Dir(path), ".wb-config-*.yaml", inj)
	if err != nil {
		return fmt.Errorf("stage config: %w", err)
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()
	if err := filewrite.ChmodFile(temporary, 0o600, temporaryName, inj); err != nil {
		_ = filewrite.Close(temporary, temporaryName, inj)
		return fmt.Errorf("protect staged config: %w", err)
	}
	encoder := yaml.NewEncoder(filewrite.Writer(temporary, temporaryName, inj))
	encoder.SetIndent(2)
	if err := encoder.Encode(document); err != nil {
		_ = filewrite.Close(temporary, temporaryName, inj)
		return fmt.Errorf("encode config: %w", err)
	}
	// Encode already flushed this complete document; Close emits no bytes.
	_ = encoder.Close()
	if err := filewrite.Sync(temporary, temporaryName, inj); err != nil {
		_ = filewrite.Close(temporary, temporaryName, inj)
		return fmt.Errorf("sync config: %w", err)
	}
	if err := filewrite.Close(temporary, temporaryName, inj); err != nil {
		return fmt.Errorf("close config: %w", err)
	}
	if err := filewrite.Rename(temporaryName, path, inj); err != nil {
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
