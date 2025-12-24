# AI Agent Instructions for fly-search

This document provides guidance for AI agents (like Claude, GPT, etc.) working on the `fly-search` project.

## Project Overview

`fly-search` is a CLI tool written in Go that searches across Concourse CI build logs efficiently. It solves the problem of manually running `fly watch` for each build when searching for specific patterns across multiple builds, jobs, and pipelines.

### Key Features

- Search across all pipelines or specific pipelines/jobs
- Intelligent two-tier caching system (build logs + metadata)
- Parallel log fetching for performance
- Regex pattern matching with context lines
- Multiple output formats (table, JSON)
- Task tracking within build logs

## Architecture

### Core Components

1. **CLI Flags** (`main.go` lines ~78-145)
   - Uses Go's `flag` package with both long and short forms
   - All flags defined in `init()` function
   - Short flags: `-t`, `-p`, `-a`, `-s`, `-j`, `-c`, `-o`, `-C`, `-u`, `-l`

2. **Caching System** (`main.go` lines ~63-605)
   - **Build Log Cache**: 24-hour TTL, stores actual log content
   - **Metadata Cache**: 5-minute TTL, stores pipeline/job/build relationships
   - Smart caching: auto-updates when higher count requested
   - Cache location: `~/.fly-search/cache/`

3. **Search Engine** (`main.go` lines ~485-650)
   - Parallel search with configurable concurrency
   - Regex pattern matching
   - Task name extraction from build plans
   - Context lines support

4. **Output Formatters** (`main.go` lines ~750+)
   - Table format with ANSI colors
   - JSON format for programmatic use
   - Build URL generation

## Important Design Decisions

### 1. Metadata Cache with Build Counts

**Problem**: Users may request different build counts (e.g., `-c 1` then `-c 10`). We need to cache intelligently without over-fetching or cache-missing.

**Solution** (lines ~240-350):
```go
type CachedMetadata struct {
    BuildCounts map[string]int  // Tracks how many builds cached per job
    // ... other fields
}
```

When loading cache:
- If `cachedCount >= requestedCount`: Use cache, slice to requested count
- If `cachedCount < requestedCount`: Fetch additional builds, update cache

**Key Code Locations**:
- Cache structure: lines ~71-76
- Smart update logic: lines ~317-343
- Cache save: lines ~581-605

### 2. Explicit `-a` / `--all-pipelines` Flag

**Problem**: Searching all pipelines is expensive (hundreds of API calls). Users shouldn't accidentally do this.

**Solution** (lines ~123-145):
- Require either `--pipeline` OR `--all-pipelines` (not both, not neither)
- Explicit opt-in prevents accidental heavy operations
- Clear error messages guide users

### 3. Progress Indicators

**Problem**: Long-running operations appear frozen without feedback.

**Solution**:
- Discovery phase: `"Discovering builds... pipeline X/Y (name)"` (line ~282)
- Search phase: `"Searching builds... X/Y"` with carriage return (line ~509)
- Uses `\r` to overwrite line in terminals
- Completion message when done (line ~526)

### 4. Cache Key Design

**Build Log Cache**: `SHA256(target + buildID)` (line ~454)
- Unique per build, never changes
- Long TTL (24 hours) - builds don't change

**Metadata Cache**: `SHA256(target)` (line ~533)
- Per target, independent of count
- Short TTL (5 minutes) - jobs/builds change frequently
- Stores `BuildCounts` map to track per-job cache depth

## Code Style Guidelines

### 1. Error Handling

```go
// Good: Provide context in errors
if err != nil {
    return fmt.Errorf("failed to fetch builds for %s/%s: %w", pipeline, job, err)
}

// Good: Silent failures for non-critical operations (during discovery)
if err != nil {
    // Silently skip - too verbose to print every error
    continue
}

// Bad: Generic errors without context
if err != nil {
    return err
}
```

### 2. Progress Messages

```go
// Good: Messages to stderr, results to stdout
fmt.Fprintf(os.Stderr, "Searching builds...\n")

// Good: Overwriting progress with \r
fmt.Fprintf(os.Stderr, "\rSearching builds... %d/%d", completed, total)

// Bad: Progress to stdout (pollutes results)
fmt.Printf("Searching...\n")
```

### 3. Flag Definitions

```go
// Good: Long and short flags
target = flag.String("target", "", "Concourse target name")
flag.StringVar(target, "t", "", "Shorthand for --target")

// Good: Clear descriptions, no redundant "default: X" in text
buildCount = flag.Int("count", 1, "Number of recent builds to search per job")
```

### 4. Caching Logic

```go
// Good: Check cache first, fallback to API
cached, err := loadFromCache(target, buildID, maxAge)
if cached != nil {
    return cached.Data
}
// Fetch from API...

// Good: Save to cache after successful fetch
if !*noCache {
    saveToCache(target, buildID, data)
}
```

## Common Tasks

### Adding a New Flag

1. Declare variable at top of `init()` function
2. Add long form: `flag.String/Int/Bool(...)`
3. Add short form: `flag.XxxVar(&variable, "x", default, "Shorthand...")`
4. Update README.md flags table
5. Add validation in `main()` if needed (lines ~120-150)

### Modifying Cache Behavior

**Important**: Always maintain backwards compatibility with existing cache files!

1. Update `CachedMetadata` or `CachedBuild` struct
2. Handle missing fields gracefully (use nil checks)
3. Update `loadMetadataCache()` and `saveMetadataCache()`
4. Consider cache version/migration if breaking changes

### Adding a New Output Format

1. Add to `outputFmt` flag validation
2. Create new function `outputXXX(results []SearchResult)`
3. Call in `main()` based on `*outputFmt` value (lines ~282-286)
4. Update README.md with examples

## Testing Guidelines

### Manual Testing Checklist

After code changes, test:

1. **Basic functionality**:
   ```bash
   ./fly-search -t TARGET -p PIPELINE -s "pattern"
   ```

2. **Caching behavior**:
   ```bash
   # First run (builds cache)
   ./fly-search -t TARGET -p PIPELINE -s "pattern" -c 5
   
   # Second run (uses cache)
   ./fly-search -t TARGET -p PIPELINE -s "pattern2" -c 5
   
   # Third run (updates cache)
   ./fly-search -t TARGET -p PIPELINE -s "pattern" -c 10
   ```

3. **All-pipelines search**:
   ```bash
   # Small count (faster)
   ./fly-search -t TARGET -a -s "pattern" -c 1
   ```

4. **Error cases**:
   ```bash
   # Should error: no pipeline or all-pipelines
   ./fly-search -t TARGET -s "pattern"
   
   # Should error: both pipeline and all-pipelines
   ./fly-search -t TARGET -p PIPELINE -a -s "pattern"
   ```

5. **Flag variations**:
   ```bash
   # Long flags
   ./fly-search --target TARGET --pipeline PIPELINE --search "pattern"
   
   # Short flags
   ./fly-search -t TARGET -p PIPELINE -s "pattern"
   
   # Mixed
   ./fly-search -t TARGET --pipeline PIPELINE -s "pattern"
   ```

### Performance Benchmarks

Expected timings (22 pipelines, ~200 jobs):

- **First `-a` search** (count=1): ~60 seconds (building metadata cache)
- **Second `-a` search** (count=1, cached): ~0.2 seconds
- **Upgrade cache** (count=10 after count=1): ~60 seconds (fetching more builds)
- **Cached with higher count**: ~instant (slicing cached builds)

## Common Pitfalls

### 1. ⚠️ Don't Include Count in Cache Keys

**Wrong**:
```go
cacheKey := fmt.Sprintf("%s/%s/%d", pipeline, job, buildCount)
```

**Right**:
```go
cacheKey := fmt.Sprintf("%s/%s", pipeline, job)
// Track count separately in BuildCounts map
```

**Why**: Including count causes cache misses when users change count value.

### 2. ⚠️ Always Write to Stderr for Progress

**Wrong**:
```go
fmt.Printf("Searching builds...\n")  // Pollutes stdout
```

**Right**:
```go
fmt.Fprintf(os.Stderr, "Searching builds...\n")
```

**Why**: Users pipe stdout to files/grep. Progress messages should go to stderr.

### 3. ⚠️ Handle Nil Maps in Cached Data

**Wrong**:
```go
buildCount := cached.BuildCounts[key]  // Panic if BuildCounts is nil!
```

**Right**:
```go
if cached.BuildCounts == nil {
    cached.BuildCounts = make(map[string]int)
}
buildCount := cached.BuildCounts[key]
```

**Why**: Old cache files may not have newer fields. Backwards compatibility matters.

### 4. ⚠️ Validate Flag Combinations Early

**Wrong**:
```go
// Check much later in code, after expensive operations
if *pipeline == "" && !*allPipelines { ... }
```

**Right**:
```go
// Validate immediately after flag.Parse() (lines ~123-145)
if *pipeline == "" && !*allPipelines {
    fmt.Fprintf(os.Stderr, "Error: either --pipeline or --all-pipelines is required\n")
    os.Exit(1)
}
```

**Why**: Fail fast with clear errors before doing any work.

## Architectural Constraints

### 1. Concourse API Limitations

- **No bulk API**: Can't get "all builds across all pipelines" in one call
- **Sequential discovery**: Must enumerate pipelines → jobs → builds
- **Rate limiting**: Be respectful, use caching aggressively

### 2. Cache Trade-offs

- **Metadata TTL = 5 minutes**: Balance freshness vs performance
- **Build log TTL = 24 hours**: Builds don't change once completed
- **No LRU eviction**: Keep it simple, rely on TTL expiration

### 3. Concurrency Limits

- **Default parallelism = 5**: Conservative to avoid overwhelming Concourse
- **User configurable**: `--parallel` flag for advanced users
- **Semaphore pattern**: Limit concurrent API calls (line ~491)

## Emergency Procedures

### Cache Corruption

If users report weird behavior:
```bash
# Clear all caches
./fly-search --clear-cache

# Rebuild from scratch
./fly-search -t TARGET -p PIPELINE -s "pattern"
```

### Performance Degradation

If searches are slow:
1. Check cache hit rate (look for "Using cached" messages)
2. Verify cache location is writable: `~/.fly-search/cache/`
3. Check cache file sizes: `du -sh ~/.fly-search/cache/`
4. Consider increasing `--parallel` value

### Memory Issues

If tool crashes with OOM:
1. Reduce `--count` value (fewer builds = less memory)
2. Reduce `--parallel` value (fewer concurrent fetches)
3. Use specific pipelines instead of `--all-pipelines`

## Future Enhancement Ideas

Potential improvements (not yet implemented):

- [ ] Persistent metadata cache with longer TTL (hour+ with smart invalidation)
- [ ] LRU cache eviction policy instead of pure TTL
- [ ] Incremental cache updates (only fetch new builds)
- [ ] Build status-aware caching (running builds = don't cache)
- [ ] Parallel pipeline discovery (currently sequential)
- [ ] Interactive mode with fuzzy search
- [ ] Real-time log streaming for running builds
- [ ] Export to CSV/HTML formats
- [ ] Configuration file support (~/.fly-search/config.yaml)

## Getting Help

When asking for help or reporting issues:

1. **Include version info**: Show `git describe` or commit hash
2. **Show full command**: Include all flags used
3. **Include timing**: How long did it take?
4. **Check cache state**: `ls -lh ~/.fly-search/cache/`
5. **Run with verbose logging**: (when we add `-v` flag)

## Related Documentation

- **README.md**: User-facing documentation with examples
- **main.go**: Primary source file, well-commented
- **Concourse API docs**: https://concourse-ci.org/api.html

---

**Last Updated**: 2025-12-24
**Maintainer**: AI-assisted development
**Go Version**: 1.16+
