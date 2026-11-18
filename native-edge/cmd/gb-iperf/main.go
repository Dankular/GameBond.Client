// gb-iperf runs one bounded benchmark from a GameBond client and emits the
// normalized gamebond.iperf.v1 report on stdout.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Dankular/GameBond.Core/iperf"
)

func main() {
	executable := flag.String("iperf", "iperf3", "iperf3-compatible executable")
	target := flag.String("target", "", "private iperf server hostname or IP")
	port := flag.Int("port", 5201, "iperf server port")
	duration := flag.Duration("duration", 10*time.Second, "test duration, 1s..2m")
	streams := flag.Int("streams", 1, "parallel streams, 1..16")
	protocol := flag.String("protocol", "tcp", "tcp or udp")
	reverse := flag.Bool("reverse", false, "measure server-to-client throughput")
	bandwidth := flag.String("bandwidth", "", "UDP send rate, e.g. 100M")
	clientID := flag.String("client-id", "", "GameBond client identifier")
	exitID := flag.String("exit-id", "", "active exit identifier")
	gatewayID := flag.String("gateway-id", "", "gateway identifier")
	topologyID := flag.String("topology-id", "", "topology identifier")
	segmentID := flag.String("segment-id", "", "L2 segment identifier")
	mode := flag.String("gateway-mode", "", "disabled, primary or passthrough")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	report, err := iperf.RunClient(ctx, iperf.ClientOptions{
		Executable: *executable, Target: *target, Port: *port, Duration: *duration,
		Streams: *streams, Protocol: *protocol, Reverse: *reverse, Bandwidth: *bandwidth,
		Endpoint: iperf.EndpointContext{ClientID: *clientID, ExitID: *exitID, GatewayID: *gatewayID,
			TopologyID: *topologyID, SegmentID: *segmentID, GatewayMode: *mode},
	})
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if encErr := enc.Encode(report); encErr != nil {
		fmt.Fprintln(os.Stderr, encErr)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
