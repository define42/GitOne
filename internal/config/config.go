package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
)

type Config struct {
	Listen         string `json:"listen"`
	Root           string `json:"root"`
	BootstrapUser  string `json:"bootstrapUser"`
	BootstrapToken string `json:"bootstrapToken"`
	PublicURL      string `json:"publicURL"`
}

func Load(path string) (Config, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return Config{}, e
	}
	var c Config
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e = d.Decode(&c); e != nil {
		return c, e
	}
	if c.Listen == "" {
		c.Listen = ":8080"
	}
	if c.Root == "" {
		return c, fmt.Errorf("root required")
	}
	return c, nil
}
