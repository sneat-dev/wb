package wbconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// SetPeersUpstream writes wb.yaml's peers.upstream section
// (peer-connectivity#req:invite-and-join), modelled on SetRemoteHub: it
// updates only the two fields `wb peers join` owns and leaves everything
// else — including the entire `remote:` block — byte-for-byte untouched.
// Remote state, claims and peer events are deliberately independent.
func SetPeersUpstream(path, hubURL, tokenFile string) error {
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
	peers := mappingValue(root, "peers")
	if peers == nil {
		peers = &yaml.Node{Kind: yaml.MappingNode}
		root.Content = append(root.Content, scalar("peers"), peers)
	} else if peers.Kind != yaml.MappingNode {
		return fmt.Errorf("parse config %s: peers must be a mapping", path)
	}
	upstream := mappingValue(peers, "upstream")
	if upstream == nil {
		upstream = &yaml.Node{Kind: yaml.MappingNode}
		peers.Content = append(peers.Content, scalar("upstream"), upstream)
	} else if upstream.Kind != yaml.MappingNode {
		return fmt.Errorf("parse config %s: peers.upstream must be a mapping", path)
	}
	setMappingScalar(upstream, "url", hubURL)
	setMappingScalar(upstream, "token_file", tokenFile)

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

// PeersUpstreamConfig is the peers.upstream section `wb peers list` reads on
// the laptop (upstream) side.
type PeersUpstreamConfig struct {
	URL       string `yaml:"url"`
	TokenFile string `yaml:"token_file"`
}

type peersConfigFile struct {
	Peers *struct {
		Upstream *PeersUpstreamConfig `yaml:"upstream"`
	} `yaml:"peers"`
}

// LoadPeersUpstream reads peers.upstream from path. found is false when the
// file, the peers section, or the upstream section is absent — the laptop
// simply has no configured hub yet, which is not an error.
func LoadPeersUpstream(path string) (PeersUpstreamConfig, bool, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return PeersUpstreamConfig{}, false, nil
	}
	if err != nil {
		return PeersUpstreamConfig{}, false, fmt.Errorf("read config %s: %w", path, err)
	}
	var file peersConfigFile
	if err := yaml.Unmarshal(raw, &file); err != nil {
		return PeersUpstreamConfig{}, false, fmt.Errorf("parse config %s: %w", path, err)
	}
	if file.Peers == nil || file.Peers.Upstream == nil {
		return PeersUpstreamConfig{}, false, nil
	}
	return *file.Peers.Upstream, true, nil
}
