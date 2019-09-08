package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	// Buffer size for incoming UDP packets
	UDP_MAX_PACKET_SIZE int = 64 * 1024

	// Max allowed size for outgoing UDP packets
	// for rationale see: https://github.com/statsd/statsd/blob/master/docs/metric_types.md#multi-metric-packets
	maxOutboundUDPSize = 512
)

var (
	version = "development"

	// metrics
	counterPacketsRecv int64
	counterPacketsSent int64
	counterOverflow    int64
	counterParseOK     int64
	counterParseFail   int64
	counterSendFail    int64
)

type regex struct {
	re    *regexp.Regexp
	regex string
	repl  []byte
}

func main() {
	log.Println("statsd-rewrite-proxy initializing. Version:", version)

	cfg, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	datagramCh := make(chan []byte, 100*UDP_MAX_PACKET_SIZE)

	go sigHandler(cancel)
	go udpListener(ctx, cfg.Listen, datagramCh)
	go printStats(cfg.StatsInterval, datagramCh)

	// start workers to process incoming UDP packets and proxy them to the target statsd server
	wg := &sync.WaitGroup{}
	for x := 0; x < cfg.Workers; x++ {
		wg.Add(1)
		go worker(ctx, wg, datagramCh, cfg.parsedRegexes, cfg.Target)
	}

	wg.Wait()
	log.Println("Exiting")
}

func sigHandler(cancel context.CancelFunc) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh,
		syscall.SIGHUP,
		syscall.SIGINT,
		syscall.SIGTERM,
		syscall.SIGQUIT)
	s := <-sigCh
	log.Printf("Received %s SIGNAL, initiating shutdown", s)
	cancel()
}

func printStats(interval int, datagramCh <-chan []byte) {
	// TODO interval from config
	ticker := time.NewTicker(time.Duration(interval) * time.Second)
	defer ticker.Stop()

	for {
		log.Printf("Pkts Recvd: %d > [Queued(bytes): %d | Overflow/Dropped: %d] > [Metrics Parsed: %d | Failed: %d] > [Pkts Sent: %d | Failed: %d]",
			atomic.LoadInt64(&counterPacketsRecv),
			int64(len(datagramCh)),
			atomic.LoadInt64(&counterOverflow),
			atomic.LoadInt64(&counterParseOK),
			atomic.LoadInt64(&counterParseFail),
			atomic.LoadInt64(&counterPacketsSent),
			atomic.LoadInt64(&counterSendFail),
		)
		<-ticker.C
	}
}

func udpListener(ctx context.Context, addr string, datagramCh chan<- []byte) {
	log.Printf("Starting StatsD UDP listener on %s", addr)

	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		log.Fatalf("Unable to parse address %s: %s", addr, err)
	}

	listener, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		log.Fatalf("Error setting up UDP listener: %s (exiting...)", err)
	}
	defer listener.Close()

	buf := make([]byte, UDP_MAX_PACKET_SIZE)

	for {
		n, _, err := listener.ReadFromUDP(buf)
		if err != nil {
			log.Fatal(err)
		}

		atomic.AddInt64(&counterPacketsRecv, 1)
		if err != nil && !strings.Contains(err.Error(), "closed network") {
			log.Printf("Error READ: %s\n", err.Error())
			continue
		}

		bufCopy := make([]byte, n)
		copy(bufCopy, buf[:n])

		select {
		case datagramCh <- bufCopy:
		default:
			atomic.AddInt64(&counterOverflow, 1)
			log.Println("Incoming StatsD message queue is full, dropping message")
		}
	}
}

func worker(ctx context.Context, wg *sync.WaitGroup, datagramCh <-chan []byte, regexes []regex, target string) {
	defer wg.Done()

	// buffer for mutated outgoing metrics
	// https://github.com/statsd/statsd/blob/master/docs/metric_types.md#multi-metric-packets
	outbound := &bytes.Buffer{}

	for {
		select {
		case <-ctx.Done():
			return
		case buf := <-datagramCh:
			// log.Println(buf)
			lines := bytes.Split(buf, []byte("\n"))

			for _, line := range lines {
				line = bytes.TrimSpace(line)

				if len(line) == 0 {
					continue
				}

				metric, err := mutateMetric(line, regexes)
				if err != nil {
					log.Println(err)
					atomic.AddInt64(&counterParseFail, 1)
					continue
				}

				atomic.AddInt64(&counterParseOK, 1)

				outbound.Write(metric)
				outbound.WriteByte('\n')

				if outbound.Len() >= maxOutboundUDPSize {
					flushBufferToUDP(outbound, target)
				}
			}
			// flush anything left in the buffer after processing a chunk of line(s)
			flushBufferToUDP(outbound, target)
		}
	}
}

// flushBufferToUDP writes the contents of buf to the UDP address in 'addr' (ip:port)
// The UDP send is best effort. The buf will be Reset() before the function returns.
//
// The caller must ensure that the size of buf fits into a single packet according to the
// MTU of the path to the addr. A reasonable "safe" value for max size is 512 bytes.
// See: https://github.com/statsd/statsd/blob/master/docs/metric_types.md#multi-metric-packets
func flushBufferToUDP(buf *bytes.Buffer, addr string) {
	if buf.Len() == 0 {
		return
	}

	// send is best effort. Reset the buffer even if the send fails so we don't block the pipeline
	defer buf.Reset()

	conn, err := net.Dial("udp", addr)
	if err != nil {
		log.Println("Failed to send metrics:", err)
		return
	}
	defer conn.Close()

	// remove trailing newline from multi-metric packets. see: https://github.com/cactus/go-statsd-client/issues/17
	payload := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))

	if _, err := conn.Write(payload); err != nil {
		log.Println("Failed to send metrics:", err)
		return
	}
	atomic.AddInt64(&counterPacketsSent, 1)
}

// mutateMetric takes a byte slice representing a single statsd metric and attempts to apply
// all of the regexp substitutions in regexes to the 'name' portion of the statsd metric.
// A possibly modified version of the metric is returned. The metric will be returned unmodified
// if none of the regexp substitutions matched.
func mutateMetric(raw []byte, regexes []regex) ([]byte, error) {
	idx := bytes.Index(raw, []byte(":"))
	if idx == -1 {
		// TODO debug only, due to large random data
		return nil, fmt.Errorf("WARN/DEBUG/TODO: '%s' not a valid statsd metric", raw)
	}

	// extract the metric name from the statsd message so that we can mutate it
	// ex: "foo.bar.baz:100|c"
	name := raw[0:idx]     // foo.bar.baz
	remainder := raw[idx:] // :100|c

	// attempt to apply all of the regexes to the metric name
	for _, r := range regexes {
		name = r.re.ReplaceAll(name, r.repl)
	}

	// re-construct and overwrite the original metric, then return
	raw = append(name, remainder...)
	return raw, nil
}
