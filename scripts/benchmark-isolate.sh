#!/bin/bash
# benchmark-isolate.sh
#
# Compares master (shared client) vs isolate branch (subprocess per torrent).
# Builds both binaries first, then runs them without switching branches.
#
# Usage:
#   ./scripts/benchmark-isolate.sh <hashes.txt> [concurrency] [iterations]
#
# Example:
#   ./scripts/benchmark-isolate.sh hashes.txt 8 3

set -e

HASHES_FILE="${1:?Usage: $0 <hashes.txt> [concurrency] [iterations]}"
CONCURRENCY="${2:-8}"
ITERATIONS="${3:-3}"
BENCH_DIR="/tmp/truespec-bench"

if [ ! -f "$HASHES_FILE" ]; then
    echo "Error: file not found: $HASHES_FILE"
    exit 1
fi

HASH_COUNT=$(grep -cv '^$\|^#' "$HASHES_FILE" 2>/dev/null || echo 0)
echo "Hashes:      $HASH_COUNT"
echo "Concurrency: $CONCURRENCY"
echo "Iterations:  $ITERATIONS"
echo ""

CURRENT_BRANCH=$(git branch --show-current)

# ── Phase 1: Build both binaries ─────────────────────────────────
echo "Building binaries..."

git checkout master >/dev/null 2>&1
go build -o "$BENCH_DIR/truespec-master" ./cmd/truespec 2>/dev/null
echo "  master  → $BENCH_DIR/truespec-master"

git checkout isolate >/dev/null 2>&1
go build -o "$BENCH_DIR/truespec-isolate" ./cmd/truespec 2>/dev/null
echo "  isolate → $BENCH_DIR/truespec-isolate"

# Restore original branch
git checkout "$CURRENT_BRANCH" >/dev/null 2>&1
echo ""

# ── Phase 2: Run benchmarks ──────────────────────────────────────

run_one() {
    local label=$1
    local bin=$2
    local iter=$3
    local outjson="$BENCH_DIR/${label}-${iter}.json"
    local outlog="$BENCH_DIR/${label}-${iter}.log"

    # Clean temp dir to avoid leftover interference
    rm -rf /tmp/truespec 2>/dev/null || true

    # Run with /usr/bin/time for RSS + CPU
    if command -v /usr/bin/time &>/dev/null; then
        /usr/bin/time -v "$bin" scan -f "$HASHES_FILE" \
            -c "$CONCURRENCY" \
            -o "$outjson" \
            -v 2>"$outlog"
    else
        { time "$bin" scan -f "$HASHES_FILE" \
            -c "$CONCURRENCY" \
            -o "$outjson" \
            -v ; } 2>"$outlog"
    fi

    # Extract metrics
    local wall=""
    local rss=""
    local cpu_user=""
    local cpu_sys=""

    if grep -q "Elapsed (wall clock)" "$outlog" 2>/dev/null; then
        wall=$(grep "Elapsed (wall clock)" "$outlog" | sed 's/.*: //')
        rss=$(grep "Maximum resident" "$outlog" | sed 's/.*: //' | tr -d ' ')
        cpu_user=$(grep "User time" "$outlog" | sed 's/.*: //')
        cpu_sys=$(grep "System time" "$outlog" | sed 's/.*: //')
    fi

    # Per-torrent elapsed from JSON (sum of all elapsed_ms)
    local total_elapsed_ms=0
    local scan_elapsed_ms=0
    if command -v jq &>/dev/null && [ -f "$outjson" ]; then
        total_elapsed_ms=$(jq '.elapsed_ms' "$outjson")
        scan_elapsed_ms=$(jq '[.results[].elapsed_ms] | add // 0' "$outjson")
    fi

    # Overhead = wall time - sum of individual torrent times (download bound)
    # This isolates the subprocess/client creation overhead
    local overhead_ms=$((total_elapsed_ms - scan_elapsed_ms))

    printf "  %-10s iter=%d  wall=%-12s  rss=%-10s  cpu_u=%-8s  cpu_s=%-8s  overhead=~%dms\n" \
        "$label" "$iter" "${wall:-N/A}" "${rss:-N/A}KB" "${cpu_user:-N/A}" "${cpu_sys:-N/A}" "$overhead_ms"

    # Save summary line for final comparison
    echo "$label $iter $wall $rss $cpu_user $cpu_sys $total_elapsed_ms $scan_elapsed_ms $overhead_ms" \
        >> "$BENCH_DIR/summary.txt"
}

# Clear previous summary
rm -f "$BENCH_DIR/summary.txt"

echo "Running benchmarks..."
echo ""

for i in $(seq 1 "$ITERATIONS"); do
    echo "── Iteration $i/$ITERATIONS ──"
    run_one "master"  "$BENCH_DIR/truespec-master"  "$i"
    run_one "isolate" "$BENCH_DIR/truespec-isolate" "$i"
    echo ""
done

# ── Phase 3: Summary ─────────────────────────────────────────────
echo "═══════════════════════════════════════════════════════"
echo "SUMMARY ($ITERATIONS iterations, $HASH_COUNT hashes, concurrency=$CONCURRENCY)"
echo "═══════════════════════════════════════════════════════"

if [ -f "$BENCH_DIR/summary.txt" ]; then
    echo ""
    printf "%-10s  %-14s  %-12s  %-10s  %-10s\n" "MODE" "WALL TIME" "RSS (KB)" "CPU USER" "OVERHEAD"
    printf "%-10s  %-14s  %-12s  %-10s  %-10s\n" "────" "─────────" "────────" "────────" "────────"

    # Average overhead per mode
    master_overhead=$(awk '$1=="master" {sum+=$9; n++} END {if(n>0) printf "%d", sum/n; else print "0"}' "$BENCH_DIR/summary.txt")
    isolate_overhead=$(awk '$1=="isolate" {sum+=$9; n++} END {if(n>0) printf "%d", sum/n; else print "0"}' "$BENCH_DIR/summary.txt")

    while read -r label iter wall rss cpu_u cpu_s total scan overhead; do
        printf "%-10s  %-14s  %-12s  %-10s  ~%dms\n" \
            "${label}[$iter]" "$wall" "${rss}KB" "$cpu_u" "$overhead"
    done < "$BENCH_DIR/summary.txt"

    echo ""
    echo "Average overhead:  master=${master_overhead}ms  isolate=${isolate_overhead}ms"

    if [ "$master_overhead" -gt 0 ] 2>/dev/null; then
        diff=$((isolate_overhead - master_overhead))
        echo "Isolation cost:    +${diff}ms per scan"
    fi
fi

echo ""
echo "Logs:    $BENCH_DIR/*.log"
echo "Results: $BENCH_DIR/*.json"
