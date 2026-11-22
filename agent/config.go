package main

// Copyright 2022 Matthew R. Wilson <mwilson@mattwilson.org>
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
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type OutputConfig struct {
	Mode           string `yaml:"mode"`
	ServiceAddress string `yaml:"service_address"`
	APIKey         string `yaml:"access_key"`
	OutputDir      string `yaml:"output_directory"`
	FontFile       string `yaml:"font_file"`
	Profile        string `yaml:"profile"`
	// Healdan is only used when Mode is "healdan".
	Healdan HealdanConfig `yaml:"healdan"`
	font    []byte
}

// HealdanConfig says where and how an output in "healdan" mode files its
// PDFs in a Healdan document management system.
type HealdanConfig struct {
	URL        string            `yaml:"url"`
	Username   string            `yaml:"username"`
	Password   string            `yaml:"password"`
	FolderID   int64             `yaml:"folder_id"`
	SpoolDir   string            `yaml:"spool_directory"`
	Properties map[string]string `yaml:"properties"`
}

type InputConfig struct {
	HerculesAddress string `yaml:"hercules_address"`
	Output          string `yaml:"output"`
}

type Configuration struct {
	InputConfig  `yaml:",inline"`
	OutputConfig `yaml:",inline"`
	Inputs       []struct {
		Name        string `yaml:"name"`
		InputConfig `yaml:",inline"`
	} `yaml:"inputs"`
	Outputs []struct {
		Name         string `yaml:"name"`
		OutputConfig `yaml:",inline"`
	} `yaml:"outputs"`
}

func loadConfig(path string) (map[string]InputConfig, map[string]OutputConfig,
	error) {

	var c Configuration
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()

	decoder := yaml.NewDecoder(f)
	if err := decoder.Decode(&c); err != nil {
		return nil, nil, err
	}

	inputs := make(map[string]InputConfig)
	c.InputConfig.Output = "default"
	inputs["default"] = c.InputConfig
	for _, i := range c.Inputs {
		if strings.TrimSpace(i.Name) == "" {
			return nil, nil, errors.New(
				"all inputs require a value in the \"name\" field")
		}
		inputs[i.Name] = i.InputConfig
	}

	outputs := make(map[string]OutputConfig)
	outputs["default"] = c.OutputConfig
	for _, o := range c.Outputs {
		if strings.TrimSpace(o.Name) == "" {
			return nil, nil, errors.New(
				"all outputs require a value in the \"name\" field")
		}
		outputs[o.Name] = o.OutputConfig
	}

	return inputs, outputs, nil
}

func validateConfig(inputs map[string]InputConfig,
	outputs map[string]OutputConfig) []error {

	var errs []error

	for name, config := range inputs {
		if config.HerculesAddress == "" {
			errs = append(errs,
				fmt.Errorf(
					"input [%s] must set 'hercules_address'",
					name))
		}

		if config.Output == "" {
			errs = append(errs,
				fmt.Errorf(
					"input [%s] must set 'output'",
					name))
		}

		if _, ok := outputs[config.Output]; !ok {
			errs = append(errs,
				fmt.Errorf(
					"input [%s] refers to output `%s`, which doesn't exist",
					name, config.Output))
		}

		// Don't allow multiple inputs to connect to the same Hercules socket
		// device.
		for othername, otherconfig := range inputs {
			if othername != name &&
				otherconfig.HerculesAddress == config.HerculesAddress {
				errs = append(errs,
					fmt.Errorf("input [%s] and input [%s] have the same "+
						"'hercules_address'; this is not allowed",
						name, othername))
			}
		}
	}

	// An output in healdan mode owns its spool directory: two printers
	// working in one would file each other's PDFs, and file them twice.
	spoolUsers := make(map[string][]string)
	for name, config := range outputs {
		if config.Mode != "healdan" {
			continue
		}
		var users []string
		for inputName, input := range inputs {
			if input.Output == name {
				users = append(users, inputName)
			}
		}
		if len(users) > 1 {
			sort.Strings(users)
			errs = append(errs, fmt.Errorf(
				"output [%s] is in 'healdan' mode, which only one input "+
					"may use, and inputs %v use it; give each its own "+
					"output and 'healdan.spool_directory'", name, users))
		}
		if config.Healdan.SpoolDir != "" {
			dir := filepath.Clean(config.Healdan.SpoolDir)
			if abs, err := filepath.Abs(dir); err == nil {
				dir = abs
			}
			spoolUsers[dir] = append(spoolUsers[dir], name)
		}
	}
	for _, names := range spoolUsers {
		if len(names) > 1 {
			sort.Strings(names)
			errs = append(errs, fmt.Errorf(
				"outputs %v have the same 'healdan.spool_directory'; "+
					"each needs its own", names))
		}
	}

	for name, config := range outputs {
		if !(config.Mode == "local" || config.Mode == "online" ||
			config.Mode == "healdan") {
			errs = append(errs,
				fmt.Errorf(
					"output [%s] 'mode' must be 'local', 'online' or "+
						"'healdan'", name))
		}

		if config.Mode == "healdan" {
			h := config.Healdan
			if h.URL == "" {
				errs = append(errs,
					fmt.Errorf("output [%s] must set 'healdan.url'", name))
			} else if u, err := url.Parse(h.URL); err != nil ||
				!(u.Scheme == "http" || u.Scheme == "https") ||
				u.Host == "" {
				errs = append(errs,
					fmt.Errorf("output [%s] 'healdan.url' must be an "+
						"http or https address", name))
			}
			if h.Username == "" {
				errs = append(errs,
					fmt.Errorf("output [%s] must set 'healdan.username'",
						name))
			}
			if h.Password == "" {
				errs = append(errs,
					fmt.Errorf("output [%s] must set 'healdan.password'",
						name))
			}
			if h.FolderID <= 0 {
				errs = append(errs,
					fmt.Errorf("output [%s] must set 'healdan.folder_id'",
						name))
			}
			if h.SpoolDir == "" {
				errs = append(errs,
					fmt.Errorf(
						"output [%s] must set 'healdan.spool_directory'",
						name))
			}
		}

		if config.Mode == "local" {
			if config.OutputDir == "" {
				errs = append(errs,
					fmt.Errorf("output [%s] must set 'output_directory'",
						name))
			}
		}

		if config.Mode == "online" {
			if config.ServiceAddress == "" {
				errs = append(errs,
					fmt.Errorf("output [%s] must set 'service_address'", name))
			}
			if config.APIKey == "" {
				errs = append(errs,
					fmt.Errorf("output [%s] must set 'api_key'", name))
			}
		}
	}

	return errs
}
