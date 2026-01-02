package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/urfave/cli/v3"
)

type SearchResult struct {
	Pipeline     string   `json:"pipeline"`
	Job          string   `json:"job"`
	BuildID      string   `json:"build_id"`
	BuildURL     string   `json:"build_url"`
	BuildURLLine string   `json:"-"` // Used for grep output, not in JSON
	Task         string   `json:"task,omitempty"`
	Line         int      `json:"line"`
	Content      string   `json:"content"`
	Context      []string `json:"context,omitempty"` // Context lines before/after match
}

type GroupedResult struct {
	Pipeline string      `json:"pipeline"`
	Job      string      `json:"job"`
	BuildID  string      `json:"build_id"`
	BuildURL string      `json:"build_url"`
	Matches  []LineMatch `json:"matches"`
}

type LineMatch struct {
	Line    int      `json:"line"`
	Content string   `json:"content"`
	Context []string `json:"context,omitempty"`
}

type Build struct {
	ID           int    `json:"id"`
	TeamName     string `json:"team_name"`
	Name         string `json:"name"`
	Status       string `json:"status"`
	JobName      string `json:"job_name"`
	PipelineName string `json:"pipeline_name"`
}

type Job struct {
	Name string `json:"name"`
}

type Pipeline struct {
	Name   string `json:"name"`
	Paused bool   `json:"paused"`
	Public bool   `json:"public"`
}

type CachedBuild struct {
	BuildID     int               `json:"build_id"`
	Logs        string            `json:"logs"`
	TaskNames   map[string]string `json:"task_names"`
	CachedAt    time.Time         `json:"cached_at"`
	BuildStatus string            `json:"build_status"`
}

type CachedMetadata struct {
	Pipelines   []Pipeline         `json:"pipelines"`
	Jobs        map[string][]Job   `json:"jobs"`         // key: pipeline_name
	Builds      map[string][]Build `json:"builds"`       // key: pipeline/job
	BuildCounts map[string]int     `json:"build_counts"` // key: pipeline/job, value: how many builds cached
	CachedAt    time.Time          `json:"cached_at"`
}

// ANSI color codes
const (
	colorReset  = "\033[0m"
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorBlue   = "\033[34m"
	colorPurple = "\033[35m"
	colorCyan   = "\033[36m"
	colorWhite  = "\033[37m"
	colorBold   = "\033[1m"
	clearToEOL  = "\033[K" // Clear from cursor to end of line
)

func main() {
	cmd := &cli.Command{
		Name:  "fly-search",
		Usage: "Search across Concourse CI build logs efficiently",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "target",
				Aliases: []string{"t"},
				Usage:   "Concourse target name (required for search)",
			},
			&cli.StringFlag{
				Name:    "pipeline",
				Aliases: []string{"p"},
				Usage:   "Pipeline name (comma-separated for multiple)",
			},
			&cli.BoolFlag{
				Name:    "all-pipelines",
				Aliases: []string{"a"},
				Usage:   "Search across ALL pipelines (can be slow!)",
			},
			&cli.StringFlag{
				Name:    "search",
				Aliases: []string{"s"},
				Usage:   "Search term/pattern (required for search)",
			},
			&cli.StringFlag{
				Name:    "job",
				Aliases: []string{"j"},
				Usage:   "Specific job name",
			},
			&cli.IntFlag{
				Name:    "count",
				Aliases: []string{"c"},
				Value:   1,
				Usage:   "Number of recent builds to search per job",
			},
			&cli.StringFlag{
				Name:    "output",
				Aliases: []string{"o"},
				Value:   "grep",
				Usage:   "Output format: grep or json",
			},
			&cli.IntFlag{
				Name:    "context",
				Aliases: []string{"C"},
				Value:   0,
				Usage:   "Number of context lines before/after match",
			},
			&cli.BoolFlag{
				Name:  "no-color",
				Usage: "Disable colorized output",
			},
			&cli.StringFlag{
				Name:    "url",
				Aliases: []string{"u"},
				Usage:   "Concourse URL for build links (auto-detected if not provided)",
			},
			&cli.StringFlag{
				Name:  "status",
				Usage: "Filter by build status (succeeded/failed/errored/aborted/pending/started)",
			},
			&cli.BoolFlag{
				Name:    "list-targets",
				Aliases: []string{"l"},
				Usage:   "List all available fly targets and exit",
			},
			&cli.IntFlag{
				Name:  "parallel",
				Value: 5,
				Usage: "Number of parallel log fetches",
			},
			&cli.DurationFlag{
				Name:  "cache-max-age",
				Value: 24 * time.Hour,
				Usage: "Maximum age of cached logs (e.g., 24h, 1h, 30m)",
			},
			&cli.DurationFlag{
				Name:  "metadata-cache-max-age",
				Value: 5 * time.Minute,
				Usage: "Maximum age of cached metadata (e.g., 5m, 10m)",
			},
			&cli.BoolFlag{
				Name:  "no-cache",
				Usage: "Disable cache (always fetch fresh)",
			},
			&cli.BoolFlag{
				Name:  "clear-cache",
				Usage: "Clear all cached logs and exit",
			},
		},
		Action: runSearch,
	}

	if err := cmd.Run(context.Background(), os.Args); err != nil {
		log.Fatal(err)
	}
}

func runSearch(ctx context.Context, cmd *cli.Command) error {
	// Handle --clear-cache flag
	if cmd.Bool("clear-cache") {
		if err := clearCacheDir(); err != nil {
			return fmt.Errorf("error clearing cache: %w", err)
		}
		fmt.Println("Cache cleared successfully")
		return nil
	}

	// Handle --list-targets flag
	if cmd.Bool("list-targets") {
		listAvailableTargets()
		return nil
	}

	// Get flag values
	target := cmd.String("target")
	pipeline := cmd.String("pipeline")
	allPipelines := cmd.Bool("all-pipelines")
	searchTerm := cmd.String("search")
	job := cmd.String("job")
	buildCount := cmd.Int("count")
	outputFmt := cmd.String("output")
	contextLines := cmd.Int("context")
	noColor := cmd.Bool("no-color")
	concourseURL := cmd.String("url")
	buildStatus := cmd.String("status")
	parallel := cmd.Int("parallel")
	cacheMaxAge := cmd.Duration("cache-max-age")
	metadataCacheMaxAge := cmd.Duration("metadata-cache-max-age")
	noCache := cmd.Bool("no-cache")

	// Validate required flags for search
	if target == "" {
		fmt.Fprintf(os.Stderr, "Error: --target is required\n\n")
		fmt.Fprintf(os.Stderr, "Available targets:\n")
		listAvailableTargets()
		fmt.Fprintf(os.Stderr, "\nUse --target flag to specify a target, or --list-targets to see more details\n\n")
		return cli.Exit("", 1)
	}

	if searchTerm == "" {
		return cli.Exit("Error: --search is required", 1)
	}

	// Require either -pipeline or -all-pipelines
	if pipeline == "" && !allPipelines {
		return cli.Exit("Error: either --pipeline or --all-pipelines is required\n\n"+
			"Use --pipeline <name> to search specific pipeline(s)\n"+
			"Use --all-pipelines to search ALL pipelines (can be slow!)", 1)
	}

	// Prevent both -pipeline and -all-pipelines
	if pipeline != "" && allPipelines {
		return cli.Exit("Error: cannot use both --pipeline and --all-pipelines", 1)
	}

	// Initialize cache directory
	if !noCache {
		if err := initCacheDir(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to initialize cache directory: %v\n", err)
			fmt.Fprintf(os.Stderr, "Continuing without cache...\n")
			noCache = true
		}
	}

	// Auto-detect Concourse URL from target if not provided
	baseURL := concourseURL
	if baseURL == "" {
		detectedURL, err := getTargetURL(target)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to auto-detect Concourse URL: %v\n", err)
			fmt.Fprintf(os.Stderr, "Build links will not be generated. Use --url flag to specify manually.\n\n")
		} else {
			baseURL = detectedURL
			fmt.Fprintf(os.Stderr, "Auto-detected Concourse URL: %s\n", baseURL)
		}
	}

	var allBuilds []Build

	// If -all-pipelines flag is set, search ALL pipelines
	if allPipelines {
		fmt.Fprintf(os.Stderr, "Searching ALL pipelines in target '%s'...\n", target)

		var pipelines []Pipeline
		var cachedJobsMap map[string][]Job
		var cachedBuildsMap map[string][]Build
		var buildCountsMap map[string]int
		var err error
		var needsUpdate bool

		// Try to load from metadata cache
		if !noCache {
			cached, cacheErr := loadMetadataCache(target, metadataCacheMaxAge)
			if cacheErr != nil {
				fmt.Fprintf(os.Stderr, "Warning: metadata cache error: %v\n", cacheErr)
			} else if cached != nil {
				fmt.Fprintf(os.Stderr, "Using cached pipeline/job/build metadata (cached %s ago)\n", time.Since(cached.CachedAt).Round(time.Second))
				pipelines = cached.Pipelines
				cachedJobsMap = cached.Jobs
				cachedBuildsMap = cached.Builds
				buildCountsMap = cached.BuildCounts
				if buildCountsMap == nil {
					buildCountsMap = make(map[string]int)
				}
			}
		}

		// If not cached, fetch fresh metadata
		if pipelines == nil {
			fmt.Fprintf(os.Stderr, "Fetching pipeline list...\n")
			pipelines, err = getAllPipelines(target)
			if err != nil {
				return fmt.Errorf("error fetching pipelines: %w", err)
			}

			if len(pipelines) == 0 {
				fmt.Println("No pipelines found")
				return nil
			}

			fmt.Fprintf(os.Stderr, "Found %d pipeline(s), fetching jobs and builds...\n", len(pipelines))

			// Fetch all jobs and builds
			cachedJobsMap = make(map[string][]Job)
			cachedBuildsMap = make(map[string][]Build)
			buildCountsMap = make(map[string]int)

			for idx, pl := range pipelines {
				// Show progress during discovery
				fmt.Fprintf(os.Stderr, "\rDiscovering builds... pipeline %d/%d (%s)%s", idx+1, len(pipelines), pl.Name, clearToEOL)

				// Get jobs for this pipeline
				jobs, err := getJobs(target, pl.Name)
				if err != nil {
					// Silently skip - will show warning at end
					continue
				}
				cachedJobsMap[pl.Name] = jobs

				// Get builds for each job
				for _, j := range jobs {
					cacheKey := fmt.Sprintf("%s/%s", pl.Name, j.Name)
					builds, err := getBuilds(target, pl.Name, j.Name, buildCount)
					if err != nil {
						// Silently skip
						continue
					}
					cachedBuildsMap[cacheKey] = builds
					buildCountsMap[cacheKey] = buildCount
				}
			}
			fmt.Fprintf(os.Stderr, "\r%s", clearToEOL) // Clear discovery progress line
			fmt.Fprintf(os.Stderr, "Discovery complete\n")
			needsUpdate = true
		} else {
			// Cache exists - check if we need more builds than cached
			// Scan through all jobs to see if any need more builds
			for _, pl := range pipelines {
				jobs := cachedJobsMap[pl.Name]
				for _, j := range jobs {
					cacheKey := fmt.Sprintf("%s/%s", pl.Name, j.Name)
					cachedCount := buildCountsMap[cacheKey]

					if cachedCount < buildCount {
						// Need to fetch more builds for this job
						if !needsUpdate {
							fmt.Fprintf(os.Stderr, "Cache has fewer builds than requested, fetching additional builds...\n")
							needsUpdate = true
						}
						builds, err := getBuilds(target, pl.Name, j.Name, buildCount)
						if err == nil {
							cachedBuildsMap[cacheKey] = builds
							buildCountsMap[cacheKey] = buildCount
						}
					}
				}
			}
		}

		// Save updated cache if needed
		if needsUpdate && !noCache {
			if err := saveMetadataCache(target, pipelines, cachedJobsMap, cachedBuildsMap, buildCountsMap); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to save metadata cache: %v\n", err)
			}
		}

		// Now extract builds based on job filter
		if job == "" {
			// All jobs in all pipelines
			for _, pl := range pipelines {
				jobs := cachedJobsMap[pl.Name]
				for _, j := range jobs {
					cacheKey := fmt.Sprintf("%s/%s", pl.Name, j.Name)
					builds := cachedBuildsMap[cacheKey]
					// Take only the requested count from cached builds
					if len(builds) > buildCount {
						builds = builds[:buildCount]
					}
					allBuilds = append(allBuilds, builds...)
				}
			}
		} else {
			// Specific job across all pipelines
			for _, pl := range pipelines {
				cacheKey := fmt.Sprintf("%s/%s", pl.Name, job)
				builds := cachedBuildsMap[cacheKey]
				// Take only the requested count from cached builds
				if len(builds) > buildCount {
					builds = builds[:buildCount]
				}
				allBuilds = append(allBuilds, builds...)
			}
		}
	} else {
		// Support multiple pipelines (comma-separated)
		pipelines := strings.Split(pipeline, ",")

		for _, p := range pipelines {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}

			// If no job specified, get all jobs in the pipeline
			if job == "" {
				jobs, err := getJobs(target, p)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)

					// Check if it's an authentication error - exit immediately
					if strings.Contains(err.Error(), "not authorized") || strings.Contains(err.Error(), "log in") {
						return cli.Exit("", 1)
					}

					// Try to show similar pipeline names
					pipelines, listErr := getAllPipelines(target)
					if listErr == nil && len(pipelines) > 0 {
						// Find pipelines with similar names
						var similar []string
						pLower := strings.ToLower(p)

						// Strategy: look for pipelines that share significant substrings
						for _, pl := range pipelines {
							plLower := strings.ToLower(pl.Name)

							// Check if they share a word/hyphenated part
							pParts := strings.Split(pLower, "-")
							plParts := strings.Split(plLower, "-")

							matchCount := 0
							for _, pp := range pParts {
								if len(pp) < 3 {
									continue // Skip very short parts
								}
								for _, plp := range plParts {
									if strings.Contains(plp, pp) || strings.Contains(pp, plp) {
										matchCount++
										break
									}
								}
							}

							// If at least one meaningful part matches, consider it similar
							if matchCount > 0 {
								similar = append(similar, pl.Name)
							}
						}

						if len(similar) > 0 {
							fmt.Fprintf(os.Stderr, "\nDid you mean:\n")
							for _, name := range similar[:min(5, len(similar))] {
								fmt.Fprintf(os.Stderr, "  - %s\n", name)
							}
						} else {
							fmt.Fprintf(os.Stderr, "\nAvailable pipelines (showing first 10):\n")
							for i := 0; i < min(10, len(pipelines)); i++ {
								fmt.Fprintf(os.Stderr, "  - %s\n", pipelines[i].Name)
							}
							if len(pipelines) > 10 {
								fmt.Fprintf(os.Stderr, "  ... and %d more\n", len(pipelines)-10)
							}
						}
						fmt.Fprintf(os.Stderr, "\nRun: fly -t %s pipelines  (to see all pipelines)\n", target)
					}
					continue
				}

				fmt.Fprintf(os.Stderr, "Fetching builds for %d job(s) in pipeline '%s'...\n", len(jobs), p)
				for idx, j := range jobs {
					fmt.Fprintf(os.Stderr, "\rFetching builds... job %d/%d (%s)%s", idx+1, len(jobs), j.Name, clearToEOL)
					builds, err := getBuilds(target, p, j.Name, buildCount)
					if err != nil {
						fmt.Fprintf(os.Stderr, "\nWarning: failed to get builds for %s/%s: %v\n", p, j.Name, err)
						continue
					}
					allBuilds = append(allBuilds, builds...)
				}
				fmt.Fprintf(os.Stderr, "\r%s", clearToEOL) // Clear the progress line
				fmt.Fprintf(os.Stderr, "Fetched builds for %d job(s)\n", len(jobs))
			} else {
				// Get builds for specific job
				builds, err := getBuilds(target, p, job, buildCount)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error fetching builds for %s/%s: %v\n", p, job, err)
					// Check if it's an authentication error - exit immediately
					if strings.Contains(err.Error(), "not authorized") || strings.Contains(err.Error(), "log in") {
						return cli.Exit("", 1)
					}
					continue
				}
				allBuilds = append(allBuilds, builds...)
			}
		}
	}

	if len(allBuilds) == 0 {
		fmt.Println("No builds found")
		return nil
	}

	fmt.Fprintf(os.Stderr, "Found %d total builds to search\n", len(allBuilds))

	// Filter by build status if specified
	if buildStatus != "" {
		filteredBuilds := []Build{}
		for _, b := range allBuilds {
			if strings.EqualFold(b.Status, buildStatus) {
				filteredBuilds = append(filteredBuilds, b)
			}
		}
		allBuilds = filteredBuilds
		if len(allBuilds) == 0 {
			fmt.Printf("No builds found with status: %s\n", buildStatus)
			return nil
		}
	}

	// Search through logs with parallelization
	results := searchLogsParallel(allBuilds, searchTerm, target, baseURL, parallel, contextLines, cacheMaxAge, noCache, noColor)

	// Output results
	if outputFmt == "json" {
		outputJSON(results)
	} else {
		outputGrep(results, searchTerm, noColor, contextLines)
	}

	return nil
}

func getCacheDir() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}
	return filepath.Join(homeDir, ".fly-search", "cache"), nil
}

func initCacheDir() error {
	cacheDir, err := getCacheDir()
	if err != nil {
		return err
	}
	return os.MkdirAll(cacheDir, 0755)
}

func clearCacheDir() error {
	cacheDir, err := getCacheDir()
	if err != nil {
		return err
	}

	// Check if directory exists
	if _, err := os.Stat(cacheDir); os.IsNotExist(err) {
		return nil // Nothing to clear
	}

	return os.RemoveAll(cacheDir)
}

func getCacheKey(target string, buildID int) string {
	// Create a hash-based cache key
	h := sha256.New()
	io.WriteString(h, fmt.Sprintf("%s-%d", target, buildID))
	return fmt.Sprintf("%x.json", h.Sum(nil))
}

func getCachePath(target string, buildID int) (string, error) {
	cacheDir, err := getCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cacheDir, getCacheKey(target, buildID)), nil
}

func loadFromCache(target string, buildID int, maxAge time.Duration) (*CachedBuild, error) {
	cachePath, err := getCachePath(target, buildID)
	if err != nil {
		return nil, err
	}

	// Check if cache file exists
	info, err := os.Stat(cachePath)
	if os.IsNotExist(err) {
		return nil, nil // Cache miss
	}
	if err != nil {
		return nil, fmt.Errorf("failed to stat cache file: %w", err)
	}

	// Read cache file
	data, err := os.ReadFile(cachePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read cache file: %w", err)
	}

	var cached CachedBuild
	if err := json.Unmarshal(data, &cached); err != nil {
		return nil, fmt.Errorf("failed to parse cache file: %w", err)
	}

	// Check if cache is too old
	if time.Since(cached.CachedAt) > maxAge {
		// Cache expired, remove it
		os.Remove(cachePath)
		return nil, nil
	}

	// Check modification time as backup
	if time.Since(info.ModTime()) > maxAge {
		os.Remove(cachePath)
		return nil, nil
	}

	return &cached, nil
}

func saveToCache(target string, buildID int, logs string, taskNames map[string]string, buildStatus string) error {
	cachePath, err := getCachePath(target, buildID)
	if err != nil {
		return err
	}

	cached := CachedBuild{
		BuildID:     buildID,
		Logs:        logs,
		TaskNames:   taskNames,
		CachedAt:    time.Now(),
		BuildStatus: buildStatus,
	}

	data, err := json.Marshal(cached)
	if err != nil {
		return fmt.Errorf("failed to marshal cache data: %w", err)
	}

	if err := os.WriteFile(cachePath, data, 0644); err != nil {
		return fmt.Errorf("failed to write cache file: %w", err)
	}

	return nil
}

func getMetadataCachePath(target string) (string, error) {
	cacheDir, err := getCacheDir()
	if err != nil {
		return "", err
	}
	// Use target name as the metadata cache file name
	hash := sha256.Sum256([]byte(target))
	return filepath.Join(cacheDir, fmt.Sprintf("metadata_%x.json", hash[:8])), nil
}

func loadMetadataCache(target string, maxAge time.Duration) (*CachedMetadata, error) {
	cachePath, err := getMetadataCachePath(target)
	if err != nil {
		return nil, err
	}

	// Check if cache file exists
	info, err := os.Stat(cachePath)
	if os.IsNotExist(err) {
		return nil, nil // Cache miss
	}
	if err != nil {
		return nil, fmt.Errorf("failed to stat metadata cache: %w", err)
	}

	// Read cache file
	data, err := os.ReadFile(cachePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read metadata cache: %w", err)
	}

	var cached CachedMetadata
	if err := json.Unmarshal(data, &cached); err != nil {
		return nil, fmt.Errorf("failed to parse metadata cache: %w", err)
	}

	// Check if cache is too old
	if time.Since(cached.CachedAt) > maxAge {
		os.Remove(cachePath)
		return nil, nil
	}

	// Check modification time as backup
	if time.Since(info.ModTime()) > maxAge {
		os.Remove(cachePath)
		return nil, nil
	}

	return &cached, nil
}

func saveMetadataCache(target string, pipelines []Pipeline, jobs map[string][]Job, builds map[string][]Build, buildCounts map[string]int) error {
	cachePath, err := getMetadataCachePath(target)
	if err != nil {
		return err
	}

	cached := CachedMetadata{
		Pipelines:   pipelines,
		Jobs:        jobs,
		Builds:      builds,
		BuildCounts: buildCounts,
		CachedAt:    time.Now(),
	}

	data, err := json.Marshal(cached)
	if err != nil {
		return fmt.Errorf("failed to marshal metadata cache: %w", err)
	}

	if err := os.WriteFile(cachePath, data, 0644); err != nil {
		return fmt.Errorf("failed to write metadata cache: %w", err)
	}

	return nil
}

func listAvailableTargets() {
	cmd := exec.Command("fly", "targets")
	output, err := cmd.Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error fetching targets: %v\n", err)
		fmt.Fprintf(os.Stderr, "Make sure 'fly' CLI is installed and you have logged in to at least one target\n")
		return
	}
	fmt.Print(string(output))
}

func getAllPipelines(target string) ([]Pipeline, error) {
	cmd := exec.Command("fly", "-t", target, "pipelines", "--json")
	output, err := cmd.CombinedOutput()
	if err != nil {
		outputStr := string(output)
		// Check for authentication errors
		if strings.Contains(outputStr, "not authorized") || strings.Contains(outputStr, "log in") {
			return nil, fmt.Errorf("not authorized. Please run: fly -t %s login", target)
		}
		return nil, fmt.Errorf("failed to get pipelines: %w\nOutput: %s", err, outputStr)
	}

	var pipelines []Pipeline
	if err := json.Unmarshal(output, &pipelines); err != nil {
		return nil, fmt.Errorf("failed to parse pipelines JSON: %w", err)
	}

	return pipelines, nil
}

func getTargetURL(target string) (string, error) {
	cmd := exec.Command("fly", "targets")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to run fly targets: %w", err)
	}

	// Parse the output to find the target URL
	// Format: targetName    URL    team    expiry
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == target {
			return fields[1], nil
		}
	}

	return "", fmt.Errorf("target '%s' not found in fly targets", target)
}

func getJobs(target, pipeline string) ([]Job, error) {
	cmd := exec.Command("fly", "-t", target, "curl", fmt.Sprintf("/api/v1/teams/main/pipelines/%s/jobs", pipeline))
	output, err := cmd.Output()

	// Check for authentication errors
	if err != nil || len(output) == 0 {
		// Get stderr for error messages
		if exitErr, ok := err.(*exec.ExitError); ok {
			stderr := string(exitErr.Stderr)
			if strings.Contains(stderr, "not authorized") || strings.Contains(stderr, "log in") {
				return nil, fmt.Errorf("not authorized. Please run: fly -t %s login", target)
			}
		}
		// Check for empty response which usually means pipeline doesn't exist
		if len(output) == 0 {
			return nil, fmt.Errorf("pipeline '%s' not found (check spelling or run: fly -t %s pipelines)", pipeline, target)
		}
	}

	var jobs []Job
	if err := json.Unmarshal(output, &jobs); err != nil {
		// Check if it's because the pipeline doesn't exist
		if strings.Contains(err.Error(), "unexpected end of JSON input") || strings.Contains(err.Error(), "invalid character") {
			return nil, fmt.Errorf("pipeline '%s' not found (check spelling or run: fly -t %s pipelines)", pipeline, target)
		}
		return nil, fmt.Errorf("failed to parse jobs JSON: %w", err)
	}

	return jobs, nil
}

func getBuilds(target, pipeline, job string, count int) ([]Build, error) {
	args := []string{"-t", target, "builds", "-p", pipeline, "--json", "-c", fmt.Sprintf("%d", count)}

	if job != "" {
		args = []string{"-t", target, "builds", "-j", fmt.Sprintf("%s/%s", pipeline, job), "--json", "-c", fmt.Sprintf("%d", count)}
	}

	cmd := exec.Command("fly", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		outputStr := string(output)
		// Check for authentication errors
		if strings.Contains(outputStr, "not authorized") || strings.Contains(outputStr, "log in") {
			return nil, fmt.Errorf("not authorized. Please run: fly -t %s login", target)
		}
		// If command fails, show the actual error from fly
		return nil, fmt.Errorf("failed to run fly builds:\n%s", outputStr)
	}

	var builds []Build
	if err := json.Unmarshal(output, &builds); err != nil {
		return nil, fmt.Errorf("failed to parse builds JSON: %w", err)
	}

	return builds, nil
}

func searchLogsParallel(builds []Build, searchTerm, target, baseURL string, parallelCount, contextLines int, cacheMaxAge time.Duration, noCache, noColor bool) []SearchResult {
	var results []SearchResult
	var mu sync.Mutex
	var wg sync.WaitGroup

	// Create a semaphore to limit parallelism
	sem := make(chan struct{}, parallelCount)

	// Counter for progress
	var completed int
	total := len(builds)

	for _, build := range builds {
		wg.Add(1)
		go func(b Build) {
			defer wg.Done()

			// Acquire semaphore
			sem <- struct{}{}
			defer func() { <-sem }()

			// Update progress with carriage return to overwrite the line
			mu.Lock()
			completed++
			fmt.Fprintf(os.Stderr, "\rSearching builds... %d/%d", completed, total)
			mu.Unlock()

			buildResults := searchSingleBuild(b, searchTerm, target, baseURL, contextLines, cacheMaxAge, noCache, noColor)

			// Thread-safe append
			mu.Lock()
			results = append(results, buildResults...)
			mu.Unlock()
		}(build)
	}

	wg.Wait()

	// Clear the progress line and move to next line
	if total > 0 {
		fmt.Fprintf(os.Stderr, "\r%-80s\r", " ") // Clear line
		fmt.Fprintf(os.Stderr, "Completed searching %d builds\n", total)
	}

	return results
}

func searchSingleBuild(build Build, searchTerm, target, baseURL string, contextLines int, cacheMaxAge time.Duration, noCache, noColor bool) []SearchResult {
	var results []SearchResult
	searchRegex := regexp.MustCompile(searchTerm)
	// Regex to detect task start: "running <task-path>"
	taskPathRegex := regexp.MustCompile(`^running\s+(.+/tasks?/[^/]+)`)

	var logs string
	var taskNames map[string]string
	var err error

	// Try to load from cache
	if !noCache {
		cached, cacheErr := loadFromCache(target, build.ID, cacheMaxAge)
		if cacheErr != nil {
			// Silently skip cache errors
		} else if cached != nil {
			logs = cached.Logs
			taskNames = cached.TaskNames
		}
	}

	// If not in cache, fetch from fly
	if logs == "" {
		// Get task names from build plan
		taskNames, err = getBuildTaskNames(target, build.ID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to get task names for build %d: %v\n", build.ID, err)
			taskNames = make(map[string]string) // Continue with empty map
		}

		logs, err = getBuildLogs(target, build.ID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to get logs for build %d: %v\n", build.ID, err)
			return results
		}

		// Save to cache
		if !noCache {
			if err := saveToCache(target, build.ID, logs, taskNames, build.Status); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to save to cache for build %d: %v\n", build.ID, err)
			}
		}
	}

	// Create ordered list of task names to map to script execution order
	orderedTaskNames := []string{}
	for _, name := range taskNames {
		orderedTaskNames = append(orderedTaskNames, name)
	}

	lines := strings.Split(logs, "\n")
	currentTask := ""
	taskIndex := 0

	for i, line := range lines {
		// Check if this line indicates a new task starting
		cleanLine := strings.TrimSpace(stripANSI(line))
		if matches := taskPathRegex.FindStringSubmatch(cleanLine); len(matches) > 1 {
			// Use the next task name from our ordered list
			if taskIndex < len(orderedTaskNames) {
				currentTask = orderedTaskNames[taskIndex]
				taskIndex++
			} else {
				currentTask = matches[1] // Fallback to script path
			}
		}

		// Check if this line matches the search
		if searchRegex.MatchString(line) {
			lineNum := i + 1
			buildURLWithLine := generateBuildURL(baseURL, build.TeamName, build.PipelineName, build.JobName, build.Name, lineNum)
			buildURLBase := generateBuildURL(baseURL, build.TeamName, build.PipelineName, build.JobName, build.Name, 0)

			// Gather context lines
			var context []string
			if contextLines > 0 {
				start := i - contextLines
				if start < 0 {
					start = 0
				}
				end := i + contextLines + 1
				if end > len(lines) {
					end = len(lines)
				}
				context = lines[start:end]
			}

			results = append(results, SearchResult{
				Pipeline:     build.PipelineName,
				Job:          build.JobName,
				BuildID:      build.Name,
				BuildURL:     buildURLBase,
				BuildURLLine: buildURLWithLine,
				Task:         currentTask,
				Line:         lineNum,
				Content:      strings.TrimRight(line, "\r\n"),
				Context:      context,
			})
		}
	}

	return results
}

type TaskInfo struct {
	ID   string
	Name string
}

func getBuildTaskNames(target string, buildID int) (map[string]string, error) {
	// Get build plan from API
	cmd := exec.Command("fly", "-t", target, "curl", fmt.Sprintf("/api/v1/builds/%d/plan", buildID))
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to get build plan: %w", err)
	}

	// Parse JSON to extract task IDs and names
	var plan struct {
		Plan json.RawMessage `json:"plan"`
	}
	if err := json.Unmarshal(output, &plan); err != nil {
		return nil, fmt.Errorf("failed to parse build plan: %w", err)
	}

	// Extract tasks from the plan
	taskMap := make(map[string]string)
	extractTasks(plan.Plan, taskMap)

	return taskMap, nil
}

func extractTasks(data json.RawMessage, taskMap map[string]string) {
	var obj map[string]interface{}
	if err := json.Unmarshal(data, &obj); err != nil {
		return
	}

	// Check if this object has a task with ID and name
	if id, hasID := obj["id"].(string); hasID {
		if taskObj, hasTask := obj["task"].(map[string]interface{}); hasTask {
			if name, hasName := taskObj["name"].(string); hasName {
				taskMap[id] = name
			}
		}
	}

	// Recursively search all nested structures
	for _, value := range obj {
		switch v := value.(type) {
		case map[string]interface{}:
			if data, err := json.Marshal(v); err == nil {
				extractTasks(data, taskMap)
			}
		case []interface{}:
			for _, item := range v {
				if data, err := json.Marshal(item); err == nil {
					extractTasks(data, taskMap)
				}
			}
		}
	}
}

func getBuildLogs(target string, buildID int) (string, error) {
	// Add 30-second timeout to prevent hanging
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "fly", "-t", target, "watch", "-b", fmt.Sprintf("%d", buildID))
	output, err := cmd.CombinedOutput()

	// Check if context deadline exceeded
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("fly watch timed out after 30s for build %d", buildID)
	}

	// fly watch returns non-zero exit code when the build failed,
	// but we still get valid log output. Only return error if output is empty.
	if err != nil && len(output) == 0 {
		return "", fmt.Errorf("fly watch failed with no output: %w", err)
	}

	return string(output), nil
}

func generateBuildURL(baseURL, team, pipeline, job, buildName string, lineNum int) string {
	if baseURL == "" {
		return ""
	}
	// Concourse URL format: https://concourse.example.com/teams/TEAM/pipelines/PIPELINE/jobs/JOB/builds/BUILD_NAME
	// BUILD_NAME is the job-specific build number (e.g., "124"), not the global build ID
	// Note: Concourse uses internal hashes for line anchors that we can't generate from fly output
	// So we just return the build URL
	return fmt.Sprintf("%s/teams/%s/pipelines/%s/jobs/%s/builds/%s",
		strings.TrimRight(baseURL, "/"), team, pipeline, job, buildName)
}

func colorize(text, color string, noColor bool) string {
	if noColor {
		return text
	}
	return color + text + colorReset
}

func highlightMatch(text, pattern string, noColor bool) string {
	if noColor {
		return text
	}
	re := regexp.MustCompile(pattern)
	return re.ReplaceAllStringFunc(text, func(match string) string {
		return colorBold + colorRed + match + colorReset
	})
}

func outputGrep(results []SearchResult, searchPattern string, noColor bool, contextLines int) {
	if len(results) == 0 {
		fmt.Println("No matches found")
		return
	}

	// Print table header
	fmt.Println()
	fmt.Println("╔═══════════════════════════════════════╦══════════╦══════╦════════════════════════════╦═══════════════════════════════════╗")
	fmt.Printf("║ %-37s ║ %-8s ║ %-4s ║ %-26s ║ %-33s ║\n", "PIPELINE / JOB", "BUILD", "LINE", "TASK", "MATCHED CONTENT")
	fmt.Println("╠═══════════════════════════════════════╬══════════╬══════╬════════════════════════════╬═══════════════════════════════════╣")

	// Print results
	for i, r := range results {
		jobPath := fmt.Sprintf("%s/%s", r.Pipeline, r.Job)
		if len(jobPath) > 37 {
			jobPath = jobPath[:34] + "..."
		}

		task := r.Task
		if task == "" {
			task = "n/a"
		} else {
			// Extract meaningful task name from path like "bbl-ci/ci/tasks/acceptance/task"
			// We want "acceptance" from the above example
			taskParts := strings.Split(task, "/")
			// Find "tasks" in the path and take the next part
			for i, part := range taskParts {
				if part == "tasks" && i+1 < len(taskParts) {
					task = taskParts[i+1]
					break
				}
			}
			// Remove trailing "/task" if present
			task = strings.TrimSuffix(task, "/task")
		}
		if len(task) > 26 {
			task = task[:23] + "..."
		}

		content := strings.TrimSpace(r.Content)
		// Strip ANSI codes for cleaner display
		content = stripANSI(content)
		// Highlight match in content
		contentDisplay := highlightMatch(content, searchPattern, noColor)
		if len(content) > 33 {
			content = content[:30] + "..."
			contentDisplay = content // Don't highlight truncated content
		}

		fmt.Printf("║ %-37s ║ %-8s ║ %-4d ║ %-26s ║ %-33s ║\n", jobPath, r.BuildID, r.Line, task, contentDisplay)

		// Print context lines if present
		if len(r.Context) > 0 && contextLines > 0 {
			fmt.Println("╠═══════════════════════════════════════╩══════════╩══════╩════════════════════════════╩═══════════════════════════════════╣")
			fmt.Println("║ " + colorCyan + "Context:" + colorReset)
			for _, ctx := range r.Context {
				cleanCtx := stripANSI(ctx)
				if len(cleanCtx) > 120 {
					cleanCtx = cleanCtx[:117] + "..."
				}
				// Highlight match in context
				if regexp.MustCompile(searchPattern).MatchString(ctx) {
					cleanCtx = highlightMatch(cleanCtx, searchPattern, noColor)
				}
				fmt.Printf("║   %s\n", cleanCtx)
			}
			fmt.Println("╠═══════════════════════════════════════╦══════════╦══════╦════════════════════════════╦═══════════════════════════════════╣")
		}

		// Print separator between rows (not after the last row)
		if i < len(results)-1 {
			fmt.Println("╠═══════════════════════════════════════╬══════════╬══════╬════════════════════════════╬═══════════════════════════════════╣")
		}
	}

	fmt.Println("╚═══════════════════════════════════════╩══════════╩══════╩════════════════════════════╩═══════════════════════════════════╝")

	// Print URLs separately if available
	if len(results) > 0 && results[0].BuildURLLine != "" {
		fmt.Println()
		fmt.Println(colorize("Build URLs:", colorBold, noColor))

		// Track unique build URLs to avoid duplicates
		printed := make(map[string]bool)
		for _, r := range results {
			if r.BuildURLLine != "" && !printed[r.BuildURLLine] {
				fmt.Printf("  [Build %s] %s\n", colorize(r.BuildID, colorGreen, noColor), r.BuildURLLine)
				printed[r.BuildURLLine] = true
			}
		}
	}

	fmt.Fprintf(os.Stderr, "\n"+colorize("Found %d match(es)", colorBold, noColor)+"\n", len(results))
}

func stripANSI(str string) string {
	// Remove ANSI escape codes
	ansiRegex := regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)
	return ansiRegex.ReplaceAllString(str, "")
}

func outputJSON(results []SearchResult) {
	// Group results by pipeline/job/build
	grouped := make(map[string]*GroupedResult)

	for _, r := range results {
		key := fmt.Sprintf("%s/%s/%s", r.Pipeline, r.Job, r.BuildID)
		if _, exists := grouped[key]; !exists {
			grouped[key] = &GroupedResult{
				Pipeline: r.Pipeline,
				Job:      r.Job,
				BuildID:  r.BuildID,
				BuildURL: r.BuildURL,
				Matches:  []LineMatch{},
			}
		}
		grouped[key].Matches = append(grouped[key].Matches, LineMatch{
			Line:    r.Line,
			Content: r.Content,
			Context: r.Context,
		})
	}

	// Convert map to slice
	var output []GroupedResult
	for _, g := range grouped {
		output = append(output, *g)
	}

	jsonOutput, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error encoding JSON: %v\n", err)
		return
	}
	fmt.Println(string(jsonOutput))
}
