package main

import (
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	TextEndpoint          string `yaml:"text_endpoint"`
	EmbeddingEndpoint     string `yaml:"embedding_endpoint"`
	PlannerExecutablePath string `yaml:"planner_executable_path"`
}

func LoadConfig(configPath string) *Config {
	cfg := &Config{}

	if configPath != "" {
		data, err := os.ReadFile(configPath)
		if err == nil {
			_ = yaml.Unmarshal(data, cfg)
		}
	}

	if envText := os.Getenv("GLLAM_TEXT_ENDPOINT"); envText != "" {
		cfg.TextEndpoint = envText
	}
	if envEmb := os.Getenv("GLLAM_EMBEDDING_ENDPOINT"); envEmb != "" {
		cfg.EmbeddingEndpoint = envEmb
	}
	if envPlanner := os.Getenv("GLLAM_PLANNER_EXECUTABLE_PATH"); envPlanner != "" {
		cfg.PlannerExecutablePath = envPlanner
	}

	return cfg
}

