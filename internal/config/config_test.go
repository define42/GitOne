package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad(t *testing.T) {
	writeConfig := func(t *testing.T, contents string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}

	t.Run("complete configuration", func(t *testing.T) {
		path := writeConfig(t, `{
			"listen": "127.0.0.1:9090",
			"root": "/srv/gitone",
			"publicURL": "https://git.example"
		}`)
		config, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if config.Listen != "127.0.0.1:9090" ||
			config.Root != "/srv/gitone" ||
			config.PublicURL != "https://git.example" {
			t.Fatalf("unexpected configuration: %#v", config)
		}
	})

	t.Run("default listen address", func(t *testing.T) {
		path := writeConfig(t, `{"root":"/srv/gitone"}`)
		config, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if config.Listen != ":8080" {
			t.Fatalf("default listen address = %q", config.Listen)
		}
	})

	for _, test := range []struct {
		name, contents, want string
	}{
		{
			name:     "missing root",
			contents: `{}`,
			want:     "root required",
		},
		{
			name:     "unknown field",
			contents: `{"root":"/srv/gitone","debug":true}`,
			want:     "unknown field",
		},
		{
			name:     "malformed JSON",
			contents: `{"root":`,
			want:     "unexpected",
		},
		{
			name:     "trailing document",
			contents: `{"root":"/srv/gitone"} {"root":"/other"}`,
			want:     "one JSON document",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, test.contents))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load() error = %v, want containing %q", err, test.want)
			}
		})
	}

	t.Run("missing file", func(t *testing.T) {
		_, err := Load(filepath.Join(t.TempDir(), "missing.json"))
		if err == nil || !os.IsNotExist(err) {
			t.Fatalf("Load() error = %v, want file-not-found", err)
		}
	})
}
