# fly-search

A CLI tool to search across multiple Concourse build logs efficiently.

## Features

- **Search ALL pipelines** - Use `-all-pipelines` flag to search across every pipeline in your target (requires explicit opt-in)
- **Intelligent caching** - Caches both build logs AND pipeline/job/build metadata for blazing fast repeated searches
  - Build logs cached for 24 hours (configurable)
  - Metadata cached for 5 minutes (configurable) - makes `-all-pipelines` extremely fast on subsequent runs
- **Parallel log fetching** - Fast concurrent search across multiple builds (configurable parallelism)
- **Search all jobs** - Search across all jobs in a pipeline or specific jobs
- **Multiple pipelines** - Search across multiple pipelines simultaneously (comma-separated)
- **Context lines** - Show N lines before/after matches for better context
- **Colorized output** - Highlighted matches and colorful formatting (can be disabled)
- **Build status filter** - Filter builds by status (succeeded, failed, errored, etc.)
- **Regex patterns** - Full regex support for complex search patterns
- **JSON output** - Structured JSON format for programmatic processing
- **Auto-detection** - Automatic Concourse URL detection from fly targets
- **Task tracking** - Shows which Concourse task generated each log line

## Prerequisites

- Go 1.16+ (for building)
- `fly` CLI installed and authenticated with your Concourse instance

## Installation

```bash
go build -o fly-search
```

Or install directly:

```bash
go install
```

## Usage

### List Available Targets

See which Concourse targets you have configured:

```bash
./fly-search -list-targets
```

### Basic Search

**Search specific pipeline:**

```bash
# Long form
./fly-search --target my-target --pipeline my-pipeline --search "test-env"

# Short form
./fly-search -t my-target -p my-pipeline -s "test-env"
```

**Search ALL pipelines in a target (requires explicit flag):**

```bash
# Long form
./fly-search --target my-target --all-pipelines --search "error"

# Short form (recommended for quick searches)
./fly-search -t my-target -a -s "error"
```

⚠️ **Warning:** Searching all pipelines can be slow on first run! Metadata is cached for 5 minutes, making subsequent searches nearly instant.

**Note:** The Concourse URL is automatically detected from your fly target, so build links are generated automatically!

### Search Specific Job

Search within a specific job:

```bash
# Long form
./fly-search --target my-target --pipeline my-pipeline --job my-job --search "test-env"

# Short form
./fly-search -t my-target -p my-pipeline -j my-job -s "test-env"
```

### Custom Build Count

By default, only the last 1 build is searched. Search through the last 20 builds:

```bash
# Long form
./fly-search --target my-target --pipeline my-pipeline --search "test-env" --count 20

# Short form
./fly-search -t my-target -p my-pipeline -s "test-env" -c 20
```

### JSON Output

Get results in JSON format with grouping:

```bash
# Long form
./fly-search --target my-target --pipeline my-pipeline --search "test-env" --output json

# Short form
./fly-search -t my-target -p my-pipeline -s "test-env" -o json
```

### Override Concourse URL (Optional)

By default, the Concourse URL is auto-detected from your fly target. You can override it:

```bash
./fly-search -target my-target -pipeline my-pipeline -search "test-env" \
  -url "https://concourse.example.com"
```

### Regex Search

Search using regex patterns:

```bash
./fly-search -target my-target -pipeline my-pipeline -search "error|failed|timeout"
```

### Context Lines

Show 3 lines before and after each match for better context:

```bash
./fly-search -target my-target -pipeline my-pipeline -search "ERROR" -context 3
```

### Filter by Build Status

Search only failed builds:

```bash
./fly-search -target my-target -pipeline my-pipeline -search "panic" -status failed
```

Available statuses: `succeeded`, `failed`, `errored`, `aborted`, `pending`, `started`

### Multiple Pipelines

**Search specific pipelines (comma-separated):**

```bash
./fly-search -target my-target -pipeline "pipeline1,pipeline2,pipeline3" -search "error"
```

**Or use `-all-pipelines` to search ALL pipelines:**

```bash
./fly-search -target my-target -all-pipelines -search "error" -count 5
```

⚠️ **Note:** You must specify either `-pipeline` OR `-all-pipelines` (not both). The `-all-pipelines` flag requires explicit opt-in to prevent accidentally heavy operations.

### Disable Colors

For piping to files or when colors aren't desired:

```bash
./fly-search -target my-target -pipeline my-pipeline -search "ERROR" -no-color
```

### Adjust Parallelism

Control how many builds are fetched in parallel (default: 5):

```bash
./fly-search -target my-target -pipeline my-pipeline -search "error" -parallel 10
```

### Cache Management

By default, build logs are cached in `~/.fly-search/cache/` for 24 hours to speed up repeated searches.

Clear all cached logs:

```bash
./fly-search -clear-cache
```

Disable caching (always fetch fresh logs):

```bash
./fly-search -target my-target -pipeline my-pipeline -search "error" -no-cache
```

Set custom cache expiration (examples: 1h, 30m, 2h30m):

```bash
./fly-search -target my-target -pipeline my-pipeline -search "error" -cache-max-age 2h
```

## Flags

### Common Flags (with shortcuts)

| Long Flag | Short | Description | Required | Default |
|-----------|-------|-------------|----------|---------|
| `--target` | `-t` | Concourse target name | Yes | - |
| `--search` | `-s` | Search term or regex pattern | Yes | - |
| `--pipeline` | `-p` | Pipeline name (comma-separated for multiple) | No* | - |
| `--all-pipelines` | `-a` | Search ALL pipelines (explicit opt-in) | No* | false |
| `--job` | `-j` | Specific job name | No | (all jobs) |
| `--count` | `-c` | Number of recent builds to search per job | No | 1 |
| `--output` | `-o` | Output format: `grep` or `json` | No | `grep` |
| `--context` | `-C` | Number of context lines before/after match | No | 0 |
| `--url` | `-u` | Concourse URL for build links | No | auto-detected |
| `--list-targets` | `-l` | List all available fly targets and exit | No | false |

*Either `--pipeline` or `--all-pipelines` is required (cannot use both)

### Advanced Flags

| Flag | Description | Default |
|------|-------------|---------|
| `--status` | Filter by build status (succeeded/failed/errored/aborted/pending/started) | (all) |
| `--no-color` | Disable colorized output | false |
| `--parallel` | Number of parallel log fetches | 5 |
| `--cache-max-age` | Maximum age of cached logs (e.g., 1h, 30m, 24h) | 24h |
| `--metadata-cache-max-age` | Maximum age of cached metadata (e.g., 5m, 10m) | 5m |
| `--no-cache` | Disable cache (always fetch fresh) | false |
| `--clear-cache` | Clear all cached logs and exit | false |

### Flag Examples

```bash
# Using long flags (more readable for scripts)
./fly-search --target prod --pipeline api --search "ERROR" --count 10

# Using short flags (faster to type)
./fly-search -t prod -p api -s "ERROR" -c 10

# Mixing long and short flags
./fly-search -t prod --all-pipelines -s "panic" -c 5

# Quick all-pipelines search
./fly-search -t prod -a -s "error"
```

## Output Formats

### Table Format (default)

Clean, structured table with build information and colorized output:

```
╔═══════════════════════════════════════╦══════════╦══════╦════════════════════════════╦═══════════════════════════════════╗
║ PIPELINE / JOB                        ║ BUILD    ║ LINE ║ TASK                       ║ MATCHED CONTENT                   ║
╠═══════════════════════════════════════╬══════════╬══════╬════════════════════════════╬═══════════════════════════════════╣
║ bosh-bootloader/gcp-acceptance-tes... ║ 124      ║ 331  ║ bbl-tests                  ║ Using state-dir: /tmp/2099801353  ║
╠═══════════════════════════════════════╩══════════╩══════╩════════════════════════════╩═══════════════════════════════════╣
║ Context:
║   
║     Captured StdOut/StdErr Output >>
║     Using state-dir: /tmp/2099801353
║     List Clusters for Zone me-central2-b: googleapi: Error 403...
║     step: generating terraform template
╠═══════════════════════════════════════╦══════════╦══════╦════════════════════════════╦═══════════════════════════════════╣
╚═══════════════════════════════════════╩══════════╩══════╩════════════════════════════╩═══════════════════════════════════╝

Build URLs:
  [Build 124] https://bosh.ci.cloudfoundry.org/teams/main/pipelines/bosh-bootloader/jobs/gcp-acceptance-tests-bump-deployments/builds/124

Found 1 match(es)
```

**Features:**
- Clean table layout with pipeline/job, build number, line number, and task name
- Matched content highlighted in red (when colors enabled)
- Context lines shown when `-context` flag is used
- Full clickable URLs listed separately (no truncation)
- ANSI color codes automatically stripped for readability
- Unique build URLs shown once (no duplicates)

### JSON Format

Structured output grouped by build, with context lines when requested:

```json
[
  {
    "pipeline": "bosh-bootloader",
    "job": "gcp-acceptance-tests-bump-deployments",
    "build_id": "124",
    "build_url": "https://bosh.ci.cloudfoundry.org/teams/main/pipelines/bosh-bootloader/jobs/gcp-acceptance-tests-bump-deployments/builds/124",
    "matches": [
      {
        "line": 331,
        "content": "  Using state-dir: /tmp/2099801353",
        "context": [
          "",
          "  Captured StdOut/StdErr Output >>",
          "  Using state-dir: /tmp/2099801353",
          "  List Clusters for Zone me-central2-b: googleapi: Error 403...",
          "  step: generating terraform template"
        ]
      }
    ]
  }
]
```

## Examples

### Search Specific Pipeline

```bash
# Long form
./fly-search --target prod --pipeline deploy --search "ERROR" --count 15

# Short form (faster to type)
./fly-search -t prod -p deploy -s "ERROR" -c 15
```

### Search ALL Pipelines

Search across every pipeline in your target:

```bash
# Long form
./fly-search --target prod --all-pipelines --search "ERROR" --count 5

# Short form (recommended)
./fly-search -t prod -a -s "ERROR" -c 5
```

⚠️ **Tip:** When using `--all-pipelines`, the first run builds metadata cache (~60s), but subsequent runs within 5 minutes are nearly instant (~0.2s)!

### Search for Environment Variables

```bash
# Long form
./fly-search --target prod --pipeline deploy --search "ENV=" --output json

# Short form
./fly-search -t prod -p deploy -s "ENV=" -o json
```

### Find Failed Tests in Specific Job

```bash
# Short form (most convenient)
./fly-search -t dev -p tests -j unit-tests -s "FAIL:"
```

### Search Only Failed Builds with Context

```bash
# Long form
./fly-search --target prod --pipeline deploy --search "panic" --status failed --context 5

# Short form
./fly-search -t prod -p deploy -s "panic" --status failed -C 5
```

### Search Across Multiple Specific Pipelines

```bash
# Short form
./fly-search -t prod -p "api,worker,frontend" -s "timeout" -c 20
```

### High-Performance Parallel Search

```bash
# Short form
./fly-search -t prod -p deploy -s "error" -c 50 --parallel 10
```

### Repeated Searches with Caching

First run fetches logs, subsequent runs use cache:

```bash
# First run (slow - fetches logs)
./fly-search -t prod -p deploy -s "pattern1" -c 20

# Second run (fast - uses cache)
./fly-search -t prod -p deploy -s "pattern2" -c 20

# Different search patterns on same builds benefit from cache
./fly-search -t prod -p deploy -s "pattern3" -c 20
```

## Performance Tips

- **Metadata caching**: When using `-all-pipelines`, the first run is slow (discovering all pipelines/jobs/builds), but subsequent runs within 5 minutes are **nearly instant**
- **Build log caching**: Logs are cached for 24 hours by default, making repeated searches very fast
- **Parallel fetching**: Increase `-parallel` value for faster searches across many builds (default: 5)
- **Specific pipelines**: Use `-pipeline` for targeted searches instead of `-all-pipelines` when possible
- **Specific jobs**: Use `-job` flag when you know which job to search (faster than searching all jobs)
- **Build status filter**: Use `-status` to narrow down builds before fetching logs
- **Lower build count**: Use `-count` to limit the number of builds searched per job (especially important with `-all-pipelines`)
- **Cache management**: Run with `-clear-cache` periodically to free disk space

## Cache Details

The tool uses two types of caching stored in `~/.fly-search/cache/`:

### Build Log Cache
- **Purpose**: Stores actual build logs to avoid re-fetching
- **Cache key**: Based on target name and build ID (SHA256 hash)
- **Default TTL**: 24 hours (configurable with `-cache-max-age`)
- **Performance gain**: **2-5x faster** than fresh fetches

### Metadata Cache (NEW!)
- **Purpose**: Stores pipeline/job/build metadata to speed up `-all-pipelines` discovery
- **Cache key**: Based on target name
- **Default TTL**: 5 minutes (configurable with `-metadata-cache-max-age`)
- **Performance gain**: **100-500x faster** for `-all-pipelines` (0.2s vs 60-300s)
- **Why 5 minutes?**: Short TTL ensures you see new builds/jobs quickly while still providing massive speedup

**Example speedup with metadata cache:**
```bash
# First run: ~60 seconds (discovering 22 pipelines, 219 builds)
./fly-search -target main -all-pipelines -count 1 -search "error"

# Second run within 5 minutes: ~0.2 seconds! (using cached metadata)
./fly-search -target main -all-pipelines -count 1 -search "panic"
```

**Cache management:**
- Expired entries are automatically removed
- Use `-clear-cache` to clear both log and metadata caches
- Use `-no-cache` to bypass cache for fresh data

## How It Works

1. Discovers builds using `fly builds` command
2. Checks cache for previously fetched logs (unless `-no-cache` specified)
3. Fetches logs in parallel using `fly watch` with configurable concurrency (if not cached)
4. Extracts task names from Concourse build plan API
5. Saves logs to cache for future searches
6. Searches logs with regex patterns
7. Tracks which task generated each log line
8. Presents results in clean table or JSON format with optional context lines

## Future Enhancements

- [ ] Stream logs in real-time for running builds
- [ ] Export results to various formats (CSV, HTML)
- [ ] Interactive mode for refining searches
- [ ] Watch mode for continuous monitoring

## Contributing

Feel free to open issues or submit pull requests!

## License

MIT
