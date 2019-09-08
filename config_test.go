package main

import (
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
)

var testConfigYAML = `
debug: true
listen: 127.0.0.1:8125
target: foobar.com:8125
workers: 4
regexes:
  - ['^vault\.', "vault.sandbox."]
`

func TestParseConfig(t *testing.T) {
	cfg, err := parseConfig([]byte(testConfigYAML))

	assert.Nil(t, err)
	assert.Equal(t, true, cfg.Debug)
	assert.Equal(t, "127.0.0.1:8125", cfg.Listen)
	assert.Equal(t, "foobar.com:8125", cfg.Target)
	assert.Equal(t, 4, cfg.Workers)
	assert.Len(t, cfg.Regexes, 1)
	assert.Len(t, cfg.parsedRegexes, 1)
}

func TestParseConfig_defaults(t *testing.T) {
	cfg, err := parseConfig([]byte(""))

	assert.Nil(t, err)
	assert.Equal(t, false, cfg.Debug)
	assert.Equal(t, "127.0.0.1:8125", cfg.Listen)
	assert.Equal(t, "", cfg.Target)
	assert.Equal(t, runtime.NumCPU(), cfg.Workers)
	assert.Equal(t, 60, cfg.StatsInterval)
	// spew.Dump(cfg)
}

func TestParseRegexes_valid(t *testing.T) {
	input := [][]string{
		{"foo", "bar"},
		{`^vault\.`, "vault.foo"},
		{`^foo\.(\w+)`, "foo.bar.$1"},
	}
	re, err := parseRegexes(input)

	assert.Nil(t, err)
	assert.NotNil(t, re)
}

func TestParseRegexes_invalid(t *testing.T) {
	input := [][]string{
		{"(?=unsupported)", "bar"},
	}
	re, err := parseRegexes(input)

	assert.Nil(t, re)
	assert.NotNil(t, err)
}
