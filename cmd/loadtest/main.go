// cmd/loadtest runs a concurrent load test against a running Zer0 Router instance.
//
// Usage:
//
//	ZER0_KEY=zer0:sk-... go run ./cmd/loadtest \
//	  -url http://localhost:8080 \
//	  -concurrency 10 \
//	  -requests 100 \
//	  -prompt "Say one word"
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type result struct {
	latency    time.Duration
	statusCode int
	provider   string
	model      string
	err        error
}

func main() {
	baseURL := flag.String("url", "http://localhost:8080", "Zer0 Router base URL")
	concurrency := flag.Int("concurrency", 5, "Concurrent workers")
	totalReqs := flag.Int("requests", 50, "Total requests to send")
	prompt := flag.String("prompt", "Say one word", "Prompt to send")
	stream := flag.Bool("stream", false, "Use streaming")
	flag.Parse()

	zer0Key := os.Getenv("ZER0_KEY")
	if zer0Key == "" {
		fmt.Fprintln(os.Stderr, "Error: ZER0_KEY env var required (zer0:sk-...)")
		os.Exit(1)
	}

	fmt.Printf("Zer0 Router load test\n")
	fmt.Printf("  URL:         %s\n", *baseURL)
	fmt.Printf("  Concurrency: %d\n", *concurrency)
	fmt.Printf("  Requests:    %d\n", *totalReqs)
	fmt.Printf("  Stream:      %v\n", *stream)
	fmt.Printf("  Prompt:      %q\n\n", *prompt)

	work := make(chan struct{}, *totalReqs)
	for i := 0; i < *totalReqs; i++ {
		work <- struct{}{}
	}
	close(work)

	results := make([]result, 0, *totalReqs)
	var mu sync.Mutex
	var wg sync.WaitGroup
	var success, failure atomic.Int64

	start := time.Now()
	for i := 0; i < *concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			client := &http.Client{Timeout: 90 * time.Second}
			for range work {
				r := sendRequest(client, *baseURL, zer0Key, *prompt, *stream)
				mu.Lock()
				results = append(results, r)
				mu.Unlock()
				if r.err == nil && r.statusCode == 200 {
					success.Add(1)
				} else {
					failure.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	// Compute latency stats
	latencies := make([]float64, 0, len(results))
	providerCounts := map[string]int{}
	for _, r := range results {
		if r.err == nil && r.statusCode == 200 {
			latencies = append(latencies, float64(r.latency.Milliseconds()))
			providerCounts[r.provider]++
		}
	}
	sort.Float64s(latencies)

	fmt.Printf("Results\n")
	fmt.Printf("  Total requests:  %d\n", *totalReqs)
	fmt.Printf("  Success:         %d (%.1f%%)\n", success.Load(), float64(success.Load())/float64(*totalReqs)*100)
	fmt.Printf("  Failure:         %d\n", failure.Load())
	fmt.Printf("  Total time:      %s\n", elapsed.Round(time.Millisecond))
	fmt.Printf("  Throughput:      %.2f req/s\n\n", float64(*totalReqs)/elapsed.Seconds())

	if len(latencies) > 0 {
		fmt.Printf("Latency (ms)\n")
		fmt.Printf("  p50:  %.0f\n", percentile(latencies, 50))
		fmt.Printf("  p75:  %.0f\n", percentile(latencies, 75))
		fmt.Printf("  p90:  %.0f\n", percentile(latencies, 90))
		fmt.Printf("  p95:  %.0f\n", percentile(latencies, 95))
		fmt.Printf("  p99:  %.0f\n", percentile(latencies, 99))
		fmt.Printf("  max:  %.0f\n\n", latencies[len(latencies)-1])
	}

	if len(providerCounts) > 0 {
		fmt.Printf("Provider distribution\n")
		for prov, count := range providerCounts {
			fmt.Printf("  %-20s %d (%.1f%%)\n", prov, count, float64(count)/float64(success.Load())*100)
		}
		fmt.Println()
	}

	// Print first few errors
	errCount := 0
	for _, r := range results {
		if r.err != nil || r.statusCode != 200 {
			if errCount < 5 {
				fmt.Printf("Error: status=%d err=%v\n", r.statusCode, r.err)
			}
			errCount++
		}
	}
	if errCount > 5 {
		fmt.Printf("  ...and %d more errors\n", errCount-5)
	}
}

func sendRequest(client *http.Client, baseURL, zer0Key, prompt string, stream bool) result {
	payload := map[string]any{
		"model":      "zer0-0.1",
		"messages":   []map[string]string{{"role": "user", "content": prompt}},
		"max_tokens": 10,
		"stream":     stream,
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequest("POST", baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return result{err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+zer0Key)

	t0 := time.Now()
	resp, err := client.Do(req)
	latency := time.Since(t0)
	if err != nil {
		return result{err: err, latency: latency}
	}
	defer resp.Body.Close()

	var out struct {
		Error *struct{ Message string } `json:"error"`
	}
	json.NewDecoder(resp.Body).Decode(&out)

	provider := resp.Header.Get("X-Zer0-Provider")
	model := resp.Header.Get("X-Zer0-Model")

	if out.Error != nil {
		return result{statusCode: resp.StatusCode, latency: latency,
			err: fmt.Errorf("%s", out.Error.Message), provider: provider, model: model}
	}
	return result{statusCode: resp.StatusCode, latency: latency, provider: provider, model: model}
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(float64(len(sorted)-1) * p / 100)
	return sorted[idx]
}
