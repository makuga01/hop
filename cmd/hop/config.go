package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func readConfig() (map[string]json.RawMessage, string, error) {
	dir, err := configDirectory()
	if err != nil {
		return nil, "", err
	}
	file := filepath.Join(dir, "config.json")
	values := map[string]json.RawMessage{}
	b, err := os.ReadFile(file)
	if os.IsNotExist(err) && isDefaultDataDir(dir) {
		if legacy := legacyDirectory(); legacy != "" {
			b, err = os.ReadFile(filepath.Join(legacy, "config.json"))
		}
	}
	if os.IsNotExist(err) {
		return values, file, nil
	}
	if err != nil {
		return nil, file, err
	}
	if err = json.Unmarshal(b, &values); err != nil || values == nil {
		return nil, file, fmt.Errorf("invalid Hop config in %s: expected a JSON object", file)
	}
	return values, file, nil
}

func configuredTheme(values map[string]json.RawMessage) (string, error) {
	name := "lagoon"
	if raw, ok := values["theme"]; ok {
		if string(raw) == "null" || json.Unmarshal(raw, &name) != nil {
			return "", errors.New("config theme must be a string: lagoon, cobalt, afterhours, or black")
		}
	}
	return themeName(name)
}

func loadTheme(override string) error {
	// An explicit override also lets a user recover from an invalid config.
	if override != "" {
		return applyTheme(override)
	}
	values, _, err := readConfig()
	if err != nil {
		return err
	}
	name, err := configuredTheme(values)
	if err != nil {
		return err
	}
	return applyTheme(name)
}

func configure(args []string) error {
	if len(args) < 1 || len(args) > 2 || args[0] != "theme" {
		return errors.New("usage: hop config theme [lagoon|cobalt|afterhours|black]")
	}
	values, file, err := readConfig()
	if err != nil {
		return err
	}
	if len(args) == 1 {
		name, err := configuredTheme(values)
		if err != nil {
			return err
		}
		fmt.Println(name)
		return nil
	}
	name, err := themeName(args[1])
	if err != nil {
		return err
	}
	values["theme"], _ = json.Marshal(name)
	b, err := json.MarshalIndent(values, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(file), ".config-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if _, err = tmp.Write(append(b, '\n')); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp.Name(), file); err != nil {
		return err
	}
	fmt.Printf("Theme saved: %s\n", name)
	return nil
}
