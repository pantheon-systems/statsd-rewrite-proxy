package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"regexp"
	"runtime"

	"gopkg.in/yaml.v1"
)

const (
	defaultConfigFile = "./statsd-rewrite-proxy.yaml"
)

type config struct {
	Debug         bool       `yaml:"debug"`
	Listen        string     `yaml:"listen"`
	Target        string     `yaml:"target"`
	Workers       int        `yaml:"workers"`
	StatsInterval int        `yaml:"stats_interval"`
	Regexes       [][]string `yaml:"regexes"`
	parsedRegexes []regex    `yaml:"-"`
}

func loadConfig() (*config, error) {
	configFile := defaultConfigFile
	if v := os.Getenv("STATSD_REWRITE_PROXY_CONFIG_FILE"); v != "" {
		configFile = v
	}
	data, err := ioutil.ReadFile(configFile)
	if err != nil {
		return nil, err
	}
	return parseConfig(data)
}

func parseConfig(blob []byte) (*config, error) {
	// Default Config:
	cfg := &config{
		Debug:         false,
		Listen:        "127.0.0.1:8125",
		Target:        "",
		Workers:       runtime.NumCPU(),
		StatsInterval: 60,
	}

	if err := yaml.Unmarshal([]byte(blob), cfg); err != nil {
		return nil, err
	}
	parsed, err := parseRegexes(cfg.Regexes)
	if err != nil {
		return nil, err
	}
	cfg.parsedRegexes = parsed
	return cfg, nil
}

func parseRegexes(list [][]string) ([]regex, error) {
	regexes := []regex{}

	for _, l := range list {
		pattern := l[0]
		repl := l[1]
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("regex pattern '%s' is invalid: %s", pattern, err)
		}
		r := regex{
			re:    re,
			regex: pattern,
			repl:  []byte(repl),
		}
		regexes = append(regexes, r)
	}
	return regexes, nil
}
