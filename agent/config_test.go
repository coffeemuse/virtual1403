package main

// Copyright 2026 CoffeeMuse
//
// This file is part of virtual1403
// <https://github.com/racingmars/virtual1403>.
//
// virtual1403 is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// virtual1403 is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with virtual1403. If not, see <https://www.gnu.org/licenses/>.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const healdanYAML = `
hercules_address: 127.0.0.1:1403
mode: healdan
profile: default-green
healdan:
  url: https://dms.example.com
  username: print-mvs1
  password: "secret"
  folder_id: 1234
  spool_directory: ./healdan-spool
  properties:
    docclass: Printouts
    author: jsmith
`

func TestLoadHealdanConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(healdanYAML), 0600); err != nil {
		t.Fatal(err)
	}
	_, outputs, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	h := outputs["default"].Healdan
	if h.URL != "https://dms.example.com" || h.Username != "print-mvs1" ||
		h.Password != "secret" || h.FolderID != 1234 ||
		h.SpoolDir != "./healdan-spool" {
		t.Errorf("unexpected healdan config: %+v", h)
	}
	if h.Properties["docclass"] != "Printouts" {
		t.Errorf("properties = %v", h.Properties)
	}
}

func validHealdanOutput() OutputConfig {
	return OutputConfig{Mode: "healdan", Healdan: HealdanConfig{
		URL: "https://dms.example.com", Username: "u", Password: "p",
		FolderID: 1234, SpoolDir: "spool",
	}}
}

func validateOne(o OutputConfig) []error {
	inputs := map[string]InputConfig{
		"default": {HerculesAddress: "127.0.0.1:1403", Output: "default"}}
	return validateConfig(inputs, map[string]OutputConfig{"default": o})
}

func TestValidateHealdan(t *testing.T) {
	if errs := validateOne(validHealdanOutput()); len(errs) != 0 {
		t.Fatalf("valid config gave errors: %v", errs)
	}
	cases := map[string]func(*HealdanConfig){
		"url":             func(h *HealdanConfig) { h.URL = "" },
		"username":        func(h *HealdanConfig) { h.Username = "" },
		"password":        func(h *HealdanConfig) { h.Password = "" },
		"folder_id":       func(h *HealdanConfig) { h.FolderID = 0 },
		"spool_directory": func(h *HealdanConfig) { h.SpoolDir = "" },
	}
	for key, blank := range cases {
		o := validHealdanOutput()
		blank(&o.Healdan)
		errs := validateOne(o)
		if len(errs) != 1 ||
			!strings.Contains(errs[0].Error(), "'healdan."+key+"'") {
			t.Errorf("%s: errors = %v", key, errs)
		}
	}
}

func TestValidateHealdanURLScheme(t *testing.T) {
	o := validHealdanOutput()
	o.Healdan.URL = "dms.example.com"
	errs := validateOne(o)
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "'healdan.url'") {
		t.Errorf("errors = %v", errs)
	}
}

func TestValidateUnknownMode(t *testing.T) {
	if errs := validateOne(OutputConfig{Mode: "bogus"}); len(errs) != 1 {
		t.Errorf("errors = %v", errs)
	}
}

func TestValidateHealdanOutputSharedByInputs(t *testing.T) {
	inputs := map[string]InputConfig{
		"default": {HerculesAddress: "127.0.0.1:1403", Output: "default"},
		"second":  {HerculesAddress: "127.0.0.1:1404", Output: "default"},
	}
	errs := validateConfig(inputs,
		map[string]OutputConfig{"default": validHealdanOutput()})
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "one input") {
		t.Errorf("errors = %v", errs)
	}
}

func TestValidateHealdanSharedSpool(t *testing.T) {
	inputs := map[string]InputConfig{
		"default": {HerculesAddress: "127.0.0.1:1403", Output: "default"},
		"second":  {HerculesAddress: "127.0.0.1:1404", Output: "other"},
	}
	other := validHealdanOutput()
	other.Healdan.SpoolDir = "./spool/" // the same directory, spelt differently
	errs := validateConfig(inputs, map[string]OutputConfig{
		"default": validHealdanOutput(), "other": other})
	if len(errs) != 1 ||
		!strings.Contains(errs[0].Error(), "'healdan.spool_directory'") {
		t.Errorf("errors = %v", errs)
	}
}
