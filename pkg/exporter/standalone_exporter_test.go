package exporter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/promslog"
	"github.com/stretchr/testify/require"
)

const fakeCLI = `#!/bin/sh
echo "$@" >> "$CALLS"
[ -n "$FAIL" ] && exit 1
case "$*" in
*summary*)
	echo 'Peer            AS        up/down state       |     #sent     #recvd'
	echo '127.0.0.1       64496     0:00:12 established          2          1'
	;;
*adj-rib*)
	echo 'neighbor 127.0.0.1 local-ip 127.0.0.1 local-as 64496 peer-as 64496 router-id 1.1.1.1 family-allowed in-open ipv4 unicast 192.168.88.248/29 next-hop self med 100'
	;;
esac
`

func newFakeExporter(t *testing.T, ttl time.Duration) (*StandaloneExporter, string) {
	t.Helper()
	dir := t.TempDir()
	cli := filepath.Join(dir, "exabgpcli")
	require.NoError(t, os.WriteFile(cli, []byte(fakeCLI), 0o755))
	calls := filepath.Join(dir, "calls")
	t.Setenv("CALLS", calls)
	e, err := NewStandaloneExporter(cli, dir, promslog.NewNopLogger())
	require.NoError(t, err)
	e.CacheTTL = ttl
	return e, calls
}

func collect(t *testing.T, e *StandaloneExporter) []prometheus.Metric {
	t.Helper()
	ch := make(chan prometheus.Metric, 100)
	e.Collect(ch)
	close(ch)
	var ms []prometheus.Metric
	for m := range ch {
		ms = append(ms, m)
	}
	return ms
}

func countCalls(t *testing.T, calls string) int {
	t.Helper()
	b, err := os.ReadFile(calls)
	if os.IsNotExist(err) {
		return 0
	}
	require.NoError(t, err)
	return strings.Count(string(b), "\n")
}

func TestStandaloneWithoutCacheRunsCLIOnEveryScrape(t *testing.T) {
	e, calls := newFakeExporter(t, 0)
	first := collect(t, e)
	collect(t, e)
	require.Equal(t, 4, countCalls(t, calls))
	require.Len(t, first, 5) // exabgp_up, peer, route, total_scrapes, parse_failures
}

func TestStandaloneCacheReusesCLIOutput(t *testing.T) {
	e, calls := newFakeExporter(t, time.Minute)
	first := collect(t, e)
	second := collect(t, e)
	require.Equal(t, 2, countCalls(t, calls))
	require.Len(t, second, len(first))
}

func TestStandaloneCacheExpires(t *testing.T) {
	e, calls := newFakeExporter(t, 50*time.Millisecond)
	collect(t, e)
	time.Sleep(100 * time.Millisecond)
	collect(t, e)
	require.Equal(t, 4, countCalls(t, calls))
}

func TestStandaloneCacheKeepsFailure(t *testing.T) {
	e, calls := newFakeExporter(t, time.Minute)
	t.Setenv("FAIL", "1")
	collect(t, e)
	collect(t, e)
	require.Equal(t, 1, countCalls(t, calls)) // summary failed, adj-rib not called, second scrape cached
	var m dto.Metric
	require.NoError(t, e.BaseExporter.parseFailures.Write(&m))
	require.Equal(t, float64(2), m.GetCounter().GetValue())
}
