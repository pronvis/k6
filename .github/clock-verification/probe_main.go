package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
	_ "unsafe"

	"go.k6.io/k6/v2/cmd/state"
	"go.k6.io/k6/v2/internal/cmd"
)

//go:linkname setWallStepNanos runtime.k6ProbeSetWallStepNanos
func setWallStepNanos(step int64)

const (
	hold       = time.Second
	step       = -5 * time.Second
	iterations = 3
	tolerance  = 10 * time.Millisecond
)

type wallStep struct {
	Before              time.Time `json:"before"`
	After               time.Time `json:"after"`
	WallDeltaNanos      int64     `json:"wall_delta_ns"`
	MonotonicDeltaNanos int64     `json:"monotonic_delta_ns"`
}

type verdict struct {
	Verdict    string    `json:"verdict"`
	Reason     string    `json:"reason"`
	HoldMillis float64   `json:"hold_ms"`
	StepMillis float64   `json:"step_ms"`
	WallStep   *wallStep `json:"wall_step"`
	Receiving  []float64 `json:"http_req_receiving_ms"`
	Duration   []float64 `json:"http_req_duration_ms"`
	K6ExitCode int       `json:"k6_exit_code"`
}

func main() {
	os.Exit(run())
}

func run() int {
	dir, err := os.MkdirTemp("", "k6-wallstep-probe")
	if err != nil {
		return inconclusive(verdict{}, err.Error())
	}
	defer os.RemoveAll(dir)
	scriptPath := filepath.Join(dir, "probe.js")
	metricsPath := filepath.Join(dir, "metrics.ndjson")
	script := fmt.Sprintf("import http from 'k6/http';\nexport const options = { vus: 1, iterations: %d };\nexport default function () { http.get(__ENV.PROBE_URL); }\n", iterations)
	if err := os.WriteFile(scriptPath, []byte(script), 0o600); err != nil {
		return inconclusive(verdict{}, err.Error())
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return inconclusive(verdict{}, err.Error())
	}
	var (
		once    sync.Once
		stepped atomic.Pointer[wallStep]
	)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "2")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		time.Sleep(hold)
		once.Do(func() {
			before := time.Now()
			setWallStepNanos(int64(step))
			after := time.Now()
			stepped.Store(&wallStep{
				Before:              before,
				After:               after,
				WallDeltaNanos:      after.Round(0).Sub(before.Round(0)).Nanoseconds(),
				MonotonicDeltaNanos: after.Sub(before).Nanoseconds(),
			})
		})
		_, _ = w.Write([]byte("ok"))
	})}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()

	gs := state.NewGlobalState(context.Background())
	gs.CmdArgs = []string{"k6", "run", "--quiet", "--summary-mode=disabled", "--out", "json=" + metricsPath, scriptPath}
	gs.Env["PROBE_URL"] = "http://" + listener.Addr().String() + "/slow"
	gs.Env["K6_NO_USAGE_REPORT"] = "true"
	exitCode := -1
	gs.OSExit = func(code int) { exitCode = code }
	cmd.ExecuteWithGlobalState(gs)

	result := verdict{HoldMillis: millis(hold), StepMillis: millis(step), WallStep: stepped.Load(), K6ExitCode: exitCode}
	if exitCode != 0 {
		return inconclusive(result, fmt.Sprintf("k6 exited with %d", exitCode))
	}
	result.Receiving, result.Duration, err = readPoints(metricsPath)
	if err != nil {
		return inconclusive(result, err.Error())
	}
	switch {
	case result.WallStep == nil:
		return inconclusive(result, "the slow endpoint was never hit, so no wall step was applied")
	case result.WallStep.WallDeltaNanos < int64(step) || result.WallStep.WallDeltaNanos > int64(step+tolerance):
		return inconclusive(result, "wall clock did not move by the requested step; the runtime overlay is not in effect")
	case result.WallStep.MonotonicDeltaNanos < 0 || result.WallStep.MonotonicDeltaNanos > int64(tolerance):
		return inconclusive(result, "monotonic clock moved with the wall step; the overlay is not wall-only")
	case len(result.Receiving) != iterations || len(result.Duration) != iterations:
		return inconclusive(result, fmt.Sprintf("expected %d http_req_receiving and http_req_duration points", iterations))
	case anyBelow(result.Receiving, 0) || anyBelow(result.Duration, 0):
		return finish(result, "RED", "k6 emitted a negative http_req_receiving or http_req_duration for a 200 response spanning a backwards wall-clock step", 1)
	case !withinBounds(result.Receiving, millis(hold-tolerance), millis(2*hold)) ||
		!withinBounds(result.Duration, millis(hold-tolerance), millis(2*hold)):
		return inconclusive(result, "durations fall outside the controlled body-hold bounds")
	}
	return finish(result, "GREEN", "all receiving and total durations stayed inside the controlled body-hold bounds across the wall-clock step", 0)
}

func readPoints(path string) (receiving, duration []float64, err error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		var line struct {
			Metric string `json:"metric"`
			Type   string `json:"type"`
			Data   struct {
				Value float64 `json:"value"`
			} `json:"data"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			return nil, nil, err
		}
		if line.Type != "Point" {
			continue
		}
		switch line.Metric {
		case "http_req_receiving":
			receiving = append(receiving, line.Data.Value)
		case "http_req_duration":
			duration = append(duration, line.Data.Value)
		}
	}
	return receiving, duration, scanner.Err()
}

func anyBelow(values []float64, floor float64) bool {
	for _, value := range values {
		if value < floor {
			return true
		}
	}
	return false
}

func withinBounds(values []float64, minimum, maximum float64) bool {
	for _, value := range values {
		if value < minimum || value > maximum {
			return false
		}
	}
	return true
}

func millis(d time.Duration) float64 {
	return float64(d) / float64(time.Millisecond)
}

func inconclusive(result verdict, reason string) int {
	return finish(result, "INCONCLUSIVE", reason, 2)
}

func finish(result verdict, name, reason string, code int) int {
	result.Verdict = name
	result.Reason = reason
	_ = json.NewEncoder(os.Stdout).Encode(result)
	fmt.Fprintf(os.Stderr, "probe: %s: %s\n", name, reason)
	return code
}
