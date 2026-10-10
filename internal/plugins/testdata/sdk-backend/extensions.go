package main

import (
	"archive/zip"
	"embed"
	"os"
	"strings"
)

//go:embed extensions/*.js extensions/*.json
var extensionFixtures embed.FS

func packageExtensions(destination, mode string) error {
	variant, id, version := "provider", "metricspanel-ext-provider-app", "1"
	if mode == "--package-extension-consumer" {
		variant = "consumer"
	}
	if mode == "--package-extension-provider-two" {
		id = "metricspanel-ext-provider-two-app"
	}
	if mode == "--package-extension-provider-v2" {
		version = "2"
	}
	if mode == "--package-extension-events" {
		variant = "events"
	}
	if mode == "--package-extension-core" {
		variant = "core"
	}
	file, err := os.Create(destination)
	if err != nil {
		return err
	}
	defer file.Close()
	archive := zip.NewWriter(file)
	files := []struct{ name, fixture string }{
		{"plugin.json", variant + ".json"},
		{"module.js", variant + ".js"},
	}
	if variant == "events" {
		files = append(files, struct{ name, fixture string }{"panel/plugin.json", "events-panel.json"}, struct{ name, fixture string }{"panel/module.js", "events-panel.js"})
	}
	for _, fileEntry := range files {
		body, err := extensionFixtures.ReadFile("extensions/" + fileEntry.fixture)
		if err != nil {
			return err
		}
		body = []byte(strings.ReplaceAll(strings.ReplaceAll(string(body), "__PROVIDER_ID__", id), "__VERSION__", version))
		entry, err := archive.Create(fileEntry.name)
		if err != nil {
			return err
		}
		if _, err = entry.Write(body); err != nil {
			return err
		}
	}
	return archive.Close()
}
