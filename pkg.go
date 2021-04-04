package main

import (
	"encoding/json"
	"io/ioutil"
	"strings"
)

type Package struct {
	Name                 string
	Repository           string   `json:"repo"`
	Source               string   `json:"source_directory"`
	ConfigurationOptions string   `json:"configure_opts"`
	Depends              []string `json:"depends"`
	Install              string   `json:"install"`
	Build                string   `json:"build"`
}

type Image struct {
	CrossCompilerLibRoot string   `json:"cross_compiler_lib_root"`
	KeyExecutables       []string `json:"executables"`
	LdSearch             []string `json:"ld_library_paths"`
}

type pkgManifest struct {
	Replacements  map[string]string `json:"replacements"`
	OauthToken    string            `json:"oauth_token"`
	Packages      []*Package        `json:"packages"`
	CrossCompiler string            `json:"cross_compiler"`
	ImageSettings Image             `json:"image_settings"`
}

func loadPackageManifest(path string) (settings pkgManifest, err error) {
	pkgFile, err := ioutil.ReadFile(path)
	if err != nil {
		return settings, err
	}

	err = json.Unmarshal([]byte(pkgFile), &settings)
	if err != nil {
		return settings, err
	}

	for k, v := range settings.Replacements {
		for i := range settings.Packages {
			settings.Packages[i].ConfigurationOptions = strings.ReplaceAll(settings.Packages[i].ConfigurationOptions, "$"+k+"$", v)
			settings.Packages[i].Build = strings.ReplaceAll(settings.Packages[i].Build, "$"+k+"$", v)
			settings.Packages[i].Install = strings.ReplaceAll(settings.Packages[i].Install, "$"+k+"$", v)
		}
	}

	for i := range settings.Packages {
		settings.Packages[i].ConfigurationOptions = strings.ReplaceAll(settings.Packages[i].ConfigurationOptions, "$cross_compiler$", settings.CrossCompiler)
		settings.Packages[i].Build = strings.ReplaceAll(settings.Packages[i].Build, "$cross_compiler$", settings.CrossCompiler)
		settings.Packages[i].Install = strings.ReplaceAll(settings.Packages[i].Install, "$cross_compiler$", settings.CrossCompiler)
	}

	return settings, nil
}
