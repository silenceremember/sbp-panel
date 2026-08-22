package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckConfigDoesNotOpenOrCreateDatabase(t *testing.T) {
	root := t.TempDir()
	database := filepath.Join(root, "panel.db")
	configPath := filepath.Join(root, "config.json")
	body := []byte(`{"database":"` + filepath.ToSlash(database) + `","listen":":9443"}`)
	if err := os.WriteFile(configPath, body, 0600); err != nil {
		t.Fatal(err)
	}

	if err := checkConfig(configPath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(database); !os.IsNotExist(err) {
		t.Fatalf("self-check touched the database: %v", err)
	}
}

func TestCheckConfigRejectsInvalidJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"listen":`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkConfig(path); err == nil {
		t.Fatal("self-check accepted invalid JSON")
	}
}
