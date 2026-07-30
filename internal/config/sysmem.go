package config

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// systemMemoryBytes returns approximate total physical RAM, or 0 if unknown.
// Prefer pure file/sysctl reads so doctor stays offline and dependency-free.
// SAMANTHA_SYS_MEM_BYTES overrides for tests and constrained hosts.
func systemMemoryBytes() uint64 {
	if v := strings.TrimSpace(os.Getenv("SAMANTHA_SYS_MEM_BYTES")); v != "" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil {
			return n
		}
	}
	switch runtime.GOOS {
	case "linux":
		return linuxMemTotal()
	case "darwin":
		return darwinMemTotal()
	default:
		return 0
	}
}

func linuxMemTotal() uint64 {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0
		}
		kb, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0
		}
		return kb * 1024
	}
	return 0
}

func darwinMemTotal() uint64 {
	return darwinMemTotalSysctl()
}

// qwenTierRAMAdvice returns a doctor diagnostic when system RAM is tight for
// the selected Qwen tier. peakGB is expected warm peak RSS order-of-magnitude
// from lab measurements (0.6B F16 ~3 GB).
func qwenTierRAMAdvice(tier string, totalBytes uint64) *Diagnostic {
	if totalBytes == 0 {
		return nil
	}
	tier = strings.ToLower(strings.TrimSpace(tier))
	if tier == "" {
		tier = "0.6b"
	}
	// Recommended free system RAM class (not model size): leave headroom for OS + brain.
	var recommendGB, peakGB float64
	switch {
	case strings.Contains(tier, "1.7"):
		recommendGB, peakGB = 16, 8 // engine may not support yet; still advise
	default:
		recommendGB, peakGB = 8, 3 // 0.6b f16 measured ~3 GB peak
	}
	totalGB := float64(totalBytes) / (1024 * 1024 * 1024)
	if totalGB >= recommendGB {
		return &Diagnostic{
			Name:     "qwen3-tts-ram",
			Severity: SeverityOK,
			Detail:   fmt.Sprintf("system RAM ~%.0f GB is enough for Qwen tier %s (warm peak ~%.0f GB class)", totalGB, tier, peakGB),
		}
	}
	return &Diagnostic{
		Name:        "qwen3-tts-ram",
		Severity:    SeverityWarn,
		Detail:      fmt.Sprintf("system RAM ~%.1f GB is tight for Qwen tier %s (warm peak ~%.0f GB; recommend ≥%.0f GB)", totalGB, tier, peakGB, recommendGB),
		Remediation: "close other heavy apps, keep qwen_tts_model_tier=0.6b, or use Kokoro on low-RAM hosts; quant GGUF is not product-ready until the lab quant gate passes",
	}
}
