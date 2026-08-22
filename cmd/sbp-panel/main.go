package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/silenceremember/sbp-panel/internal/agent"
	"github.com/silenceremember/sbp-panel/internal/config"
	"github.com/silenceremember/sbp-panel/internal/panel"
	"github.com/silenceremember/sbp-panel/internal/store"
)

func main() {
	defaultMode := "serve"
	if filepath.Base(os.Args[0]) == "sbp-panel-update" {
		defaultMode = "update"
	}
	mode := flag.String("mode", defaultMode, "serve, agent or update")
	config := flag.String("config", "/etc/vpn-panel/config.json", "configuration path")
	flag.Parse()

	var err error
	switch *mode {
	case "serve":
		err = panel.Run(*config)
	case "agent":
		err = agent.Run(*config)
	case "update-watchdog":
		err = agent.RunUpdateWatchdog(*config)
	case "update":
		err = agent.RunUpdateClient(*config, os.Stdout)
	case "self-check":
		err = checkConfig(*config)
	case "has-owner":
		err = hasOwner(*config)
	case "init-owner":
		err = initOwner(*config, os.Stdin)
	default:
		err = fmt.Errorf("unknown mode %q", *mode)
	}
	if err != nil {
		log.Printf("fatal: %v", err)
		os.Exit(1)
	}
}

func checkConfig(configPath string) error {
	_, err := config.Load(configPath)
	return err
}

func openStore(configPath string) (*store.Store, error) {
	c, err := config.Load(configPath)
	if err != nil {
		return nil, err
	}
	return store.Open(c.Database)
}

func hasOwner(configPath string) error {
	db, err := openStore(configPath)
	if err != nil {
		return err
	}
	defer db.DB.Close()
	exists, err := db.OwnerExists()
	if err != nil {
		return err
	}
	if !exists {
		os.Exit(2)
	}
	return nil
}

func initOwner(configPath string, input io.Reader) error {
	db, err := openStore(configPath)
	if err != nil {
		return err
	}
	defer db.DB.Close()
	exists, err := db.OwnerExists()
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	password, err := bufio.NewReader(io.LimitReader(input, 1024)).ReadString('\n')
	if err != nil && err != io.EOF {
		return err
	}
	if err := db.CreateOwner("admin", strings.TrimSpace(password)); err != nil {
		return err
	}
	_, _ = db.DB.Exec(`DELETE FROM settings WHERE key='bootstrap_hash'`)
	return nil
}
