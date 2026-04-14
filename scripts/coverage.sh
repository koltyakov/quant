#!/usr/bin/env bash

set -euo pipefail

profile="${COVER_PROFILE:-coverage.out}"
html="${COVER_HTML:-coverage.html}"
tmpdir="$(mktemp -d "${TMPDIR:-/tmp}/quant-cov.XXXXXX")"
combined="$tmpdir/combined.out"
summary="$tmpdir/packages.tsv"
failures="$tmpdir/failures.txt"
packages="$tmpdir/packages.txt"
status=0
failed_packages=0

if [[ -t 1 && -z "${NO_COLOR:-}" ]]; then
  c_reset=$'\033[0m'
  c_dim=$'\033[2m'
  c_bold=$'\033[1m'
  c_red=$'\033[31m'
  c_yellow=$'\033[33m'
  c_green=$'\033[32m'
  c_cyan=$'\033[36m'
else
  c_reset=""
  c_dim=""
  c_bold=""
  c_red=""
  c_yellow=""
  c_green=""
  c_cyan=""
fi

cleanup() {
  rm -rf "$tmpdir"
}
trap cleanup EXIT

mkdir -p "$(dirname "$profile")" "$(dirname "$html")"

num_only() {
  printf '%s' "${1%%%}"
}

short_pkg() {
  local pkg="$1"
  if [[ -n "${module_path:-}" ]]; then
    pkg="${pkg#"$module_path"/}"
    pkg="${pkg#"$module_path"}"
  fi
  printf '%s' "$pkg"
}

repeat_char() {
  local char="$1"
  local count="$2"
  if (( count <= 0 )); then
    return
  fi
  printf "%${count}s" "" | tr ' ' "$char"
}

bar_for_pct() {
  local pct="$1"
  local width="${2:-24}"
  awk -v pct="$(num_only "$pct")" -v width="$width" '
    BEGIN {
      filled = int((pct * width / 100) + 0.5)
      if (filled < 0) filled = 0
      if (filled > width) filled = width
      for (i = 0; i < filled; i++) printf "="
      for (i = filled; i < width; i++) printf "."
    }
  '
}

color_for_pct() {
  local pct
  pct="$(num_only "$1")"
  awk -v pct="$pct" -v red="$c_red" -v yellow="$c_yellow" -v green="$c_green" '
    BEGIN {
      if (pct < 50) {
        print red
      } else if (pct < 80) {
        print yellow
      } else {
        print green
      }
    }
  '
}

term_width() {
  local cols
  cols="$(tput cols 2>/dev/null || true)"
  if [[ "$cols" =~ ^[0-9]+$ ]] && (( cols >= 72 )); then
    printf '%s' "$cols"
    return
  fi
  printf '100'
}

print_rule() {
  local width="$1"
  printf '%s\n' "$(repeat_char "-" "$width")"
}

print_metric() {
  local label="$1"
  local value="$2"
  local extra="${3:-}"
  printf "%b%-18s%b %s" "$c_dim" "$label" "$c_reset" "$value"
  if [[ -n "$extra" ]]; then
    printf "  %s" "$extra"
  fi
  printf '\n'
}

print_band() {
  local label="$1"
  local count="$2"
  local total="$3"
  local width="$4"
  local bar
  bar="$(awk -v count="$count" -v total="$total" -v width="$width" '
    BEGIN {
      filled = 0
      if (total > 0) {
        filled = int((count * width / total) + 0.5)
      }
      if (filled < 0) filled = 0
      if (filled > width) filled = width
      for (i = 0; i < filled; i++) printf "#"
      for (i = filled; i < width; i++) printf "."
    }
  ')"
  printf "  %-9s %3s  %s\n" "$label" "$count" "$bar"
}

printf 'mode: set\n' > "$combined"
: > "$summary"
: > "$failures"
go list ./... > "$packages"
module_path="$(go list -m -f '{{.Path}}')"
pkg_total="$(wc -l < "$packages" | tr -d ' ')"
progress_width="$(term_width)"
pkg_index=0

while IFS= read -r pkg; do
  pkg_index=$((pkg_index + 1))
  safe_name="$(printf '%s' "$pkg" | tr '/.' '__')"
  pkg_profile="$tmpdir/$safe_name.out"
  pkg_log="$tmpdir/$safe_name.log"
  display_pkg="$(short_pkg "$pkg")"

  if [[ -t 1 ]]; then
    printf '\r%b[%02d/%02d]%b %-60s' "$c_cyan" "$pkg_index" "$pkg_total" "$c_reset" "$display_pkg"
  fi

  if go test -coverprofile="$pkg_profile" "$pkg" >"$pkg_log" 2>&1; then
    coverage="$(go tool cover -func="$pkg_profile" | awk '/^total:/ { print $3 }')"
    printf '%s\t%s\n' "$coverage" "$pkg" >> "$summary"
    tail -n +2 "$pkg_profile" >> "$combined"
  else
    status=1
    failed_packages=$((failed_packages + 1))
    {
      printf '%s\n' "$pkg"
      sed -n '1,20p' "$pkg_log"
      printf '\n'
    } >> "$failures"
  fi
done < "$packages"

if [[ -t 1 ]]; then
  printf '\r%*s\r' "$progress_width" ''
fi

cp "$combined" "$profile"

total_coverage="$(go tool cover -func="$profile" | awk '/^total:/ { print $3 }')"
pkg_count="$(wc -l < "$summary" | tr -d ' ')"
under_50="$(awk -F '\t' '{ gsub(/%/, "", $1); if ($1 + 0 < 50) count++ } END { print count + 0 }' "$summary")"
under_80="$(awk -F '\t' '{ gsub(/%/, "", $1); if ($1 + 0 >= 50 && $1 + 0 < 80) count++ } END { print count + 0 }' "$summary")"
at_least_80="$(awk -F '\t' '{ gsub(/%/, "", $1); if ($1 + 0 >= 80) count++ } END { print count + 0 }' "$summary")"
width="$(term_width)"
bar_width=24
band_width=20
if (( width < 90 )); then
  bar_width=18
  band_width=14
fi

printf "%bCoverage Report%b\n" "$c_bold" "$c_reset"
print_rule "$width"
print_metric "Total coverage" "$(printf '%b%s%b' "$(color_for_pct "$total_coverage")" "$total_coverage" "$c_reset")" "[$(bar_for_pct "$total_coverage" "$bar_width")]"
print_metric "Packages covered" "$pkg_count/$pkg_total"
print_metric "Failed packages" "$failed_packages"
print_metric "HTML report" "$html"

printf '\n%bCoverage Bands%b\n' "$c_bold" "$c_reset"
print_rule "$width"
print_band "<50%" "$under_50" "$pkg_count" "$band_width"
print_band "50-79%" "$under_80" "$pkg_count" "$band_width"
print_band "80%+" "$at_least_80" "$pkg_count" "$band_width"

printf '\n%bLowest-Covered Packages%b\n' "$c_bold" "$c_reset"
print_rule "$width"
awk -F '\t' -v limit=12 -v module="$module_path" -v width="$bar_width" '
  function trim_module(pkg) {
    sub("^" module "/?", "", pkg)
    return pkg
  }
  function bar_for(pct,    filled, i, out) {
    filled = int((pct * width / 100) + 0.5)
    if (filled < 0) filled = 0
    if (filled > width) filled = width
    out = ""
    for (i = 0; i < filled; i++) out = out "="
    for (i = filled; i < width; i++) out = out "."
    return out
  }
  {
    gsub(/%/, "", $1)
    rows[++n] = sprintf("%6.1f%%  [%s]  %s", $1 + 0, bar_for($1 + 0), trim_module($2))
    values[n] = $1 + 0
  }
  END {
    count = n < limit ? n : limit
    for (i = 1; i <= count; i++) print rows[i]
  }
' < <(sort -n "$summary")

if [[ -s "$failures" ]]; then
  printf '\n%bFailed Packages%b\n' "$c_bold" "$c_reset"
  print_rule "$width"
  cat "$failures"
fi

go tool cover -html="$profile" -o "$html"
printf '\n%bWrote%b %s and %s\n' "$c_dim" "$c_reset" "$profile" "$html"

exit "$status"
