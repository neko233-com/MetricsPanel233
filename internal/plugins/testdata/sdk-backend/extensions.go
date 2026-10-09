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
	file, err := os.Create(destination)
	if err != nil {
		return err
	}
	defer file.Close()
	archive := zip.NewWriter(file)
	for _, extension := range []string{"json", "js"} {
		body, err := extensionFixtures.ReadFile("extensions/" + variant + "." + extension)
		if err != nil {
			return err
		}
		body = []byte(strings.ReplaceAll(strings.ReplaceAll(string(body), "__PROVIDER_ID__", id), "__VERSION__", version))
		name := "module.js"
		if extension == "json" {
			name = "plugin.json"
		}
		entry, err := archive.Create(name)
		if err != nil {
			return err
		}
		if _, err = entry.Write(body); err != nil {
			return err
		}
	}
	return archive.Close()
}
