package main

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupUDPServer() (*net.UDPConn, error) {
	laddr, err := net.ResolveUDPAddr("udp", ":0")
	if err != nil {
		return nil, err
	}

	listener, err := net.ListenUDP("udp", laddr)
	if err != nil {
		return nil, err
	}
	return listener, nil
}

func TestWorker(t *testing.T) {
	udpServer, err := setupUDPServer()
	if err != nil {
		t.Log(err)
		t.FailNow()
	}
	defer udpServer.Close()

	regexes, err := parseRegexes([][]string{
		{`^vault\.`, "vault.prod."},
	})
	require.Nil(t, err)

	wg := &sync.WaitGroup{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// datagramCh mocks an incoming UDP listener that accepts statsd-formatted metrics and
	// feeds them into a worker() instance
	datagramCh := make(chan []byte, UDP_MAX_PACKET_SIZE)

	wg.Add(1)
	go worker(ctx, wg, datagramCh, regexes, udpServer.LocalAddr().String())

	tests := []struct {
		name     string
		incoming string
		expected string
	}{
		{
			name:     "packet with single metric",
			incoming: "vault.foo.bar:100|c",
			expected: "vault.prod.foo.bar:100|c",
		},
		{
			name:     "packet with multiple metrics",
			incoming: "vault.foo.bar:100|c\nbaz.bar:1|c\ncpu.load:1.0|g",
			expected: "vault.prod.foo.bar:100|c\nbaz.bar:1|c\ncpu.load:1.0|g",
		},
	}

	for _, test := range tests {
		datagramCh <- []byte(test.incoming)

		buf := make([]byte, UDP_MAX_PACKET_SIZE)
		n, _, err := udpServer.ReadFromUDP(buf)

		assert.Nil(t, err)
		assert.Equal(t, []byte(test.expected), buf[:n])
	}

	cancel()
	wg.Wait()
}

func TestMutateMetric(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
		err      error
		regexes  [][]string
	}{
		{
			name:     "simple match",
			input:    "vault.foo.bar:100|c",
			expected: "vault.sandbox.foo.bar:100|c",
			regexes: [][]string{
				{`^vault\.`, `vault.sandbox.`},
			},
		},
		{
			name:     "no match",
			input:    "vault.foo.bar:100|c",
			expected: "vault.foo.bar:100|c",
			regexes: [][]string{
				{`no-such-string`, ``},
			},
		},
		{
			name:     "pass thru / no regexes",
			input:    "vault.foo.bar:100|c",
			expected: "vault.foo.bar:100|c",
			regexes:  [][]string{},
		},
		{
			name:     "multiple match",
			input:    "vault.foo.bar:100|c",
			expected: "vault.sandbox.baz.bar:100|c",
			regexes: [][]string{
				{`^vault\.`, `vault.sandbox.`},
				{`foo`, `baz`},
			},
		},
		{
			name:     "numbered capture group",
			input:    "vault.foo.bar:100|c",
			expected: "prefix-vault.foo.bar:100|c",
			regexes: [][]string{
				{`^(\w+)\.`, `prefix-$1.`},
			},
		},
		{
			name:  "invalid metric",
			input: "vault",
			err:   errors.New("WARN/DEBUG/TODO: 'vault' not a valid statsd metric"),
		},
	}

	for _, test := range tests {
		regexes, err := parseRegexes(test.regexes)
		if err != nil {
			t.FailNow()
		}

		out, err := mutateMetric([]byte(test.input), regexes)
		assert.Equal(t, err, test.err, test.name)
		if test.expected != "" {
			assert.Equal(t, []byte(test.expected), out, test.name)
		}
	}
}

// func TestMain(t *testing.T) {
// // 	var err error

// // 	testConfigYAML := `
// // debug: true
// // listen: 127.0.0.1:8125
// // target: foobar.com:8125
// // workers: 4
// // regexes:
// //   - ['^vault\.', "vault.sandbox."]
// // `
// // 	file, err := ioutil.TempFile("", "testconfig")
// // 	if err != nil {
// // 		t.FailNow(err)
// // 	}
// // 	defer os.Remove(file.Name())

// // 	err = os.Setenv("STATSD_REWRITE_PROXY_CONFIG_FILE", file.Name())
// // 	require.Nil(t, err)

// }
