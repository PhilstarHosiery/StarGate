package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config holds all application configuration loaded from a YAML file.
type Config struct {
	Server struct {
		GRPCAddr    string `yaml:"grpc_addr"`    // e.g. ":50051"
		WebhookAddr string `yaml:"webhook_addr"` // e.g. ":8080"
	} `yaml:"server"`
	Database struct {
		Path string `yaml:"path"` // e.g. "stargate.db"
	} `yaml:"database"`
	SMS struct {
		GateURL string `yaml:"gate_url"` // SMS Gate / RUT241 API base URL
		APIKey  string `yaml:"api_key"`
	} `yaml:"sms"`
}

// Load reads and parses the YAML config file at path.
func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("config: open %q: %w", path, err)
	}
	defer f.Close()

	var cfg Config
	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("config: decode %q: %w", path, err)
	}
	return &cfg, nil
}
