package usage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	usageStatsFileName   = "usage_stats.json"
	autoSaveInterval     = 1 * time.Minute
	defaultMaxDetailsAge = 30 * 24 * time.Hour // 30 days
)

var (
	persistenceMu   sync.Mutex
	persistencePath string
	stopAutoSave    chan struct{}
	autoSaveRunning bool
)

// persistedData is the JSON structure saved to disk.
type persistedData struct {
	TotalRequests  int64                       `json:"total_requests"`
	SuccessCount   int64                       `json:"success_count"`
	FailureCount   int64                       `json:"failure_count"`
	TotalTokens    int64                       `json:"total_tokens"`
	APIs           map[string]*persistedAPI    `json:"apis"`
	RequestsByDay  map[string]int64            `json:"requests_by_day"`
	RequestsByHour map[int]int64               `json:"requests_by_hour"`
	TokensByDay    map[string]int64            `json:"tokens_by_day"`
	TokensByHour   map[int]int64               `json:"tokens_by_hour"`
	LastSaved      time.Time                   `json:"last_saved"`
}

type persistedAPI struct {
	TotalRequests int64                     `json:"total_requests"`
	TotalTokens   int64                     `json:"total_tokens"`
	Models        map[string]*persistedModel `json:"models"`
}

type persistedModel struct {
	TotalRequests int64           `json:"total_requests"`
	TotalTokens   int64           `json:"total_tokens"`
	Details       []RequestDetail `json:"details"`
}

// SetPersistencePath configures the directory where usage_stats.json will be stored.
// This should be called during server startup with the auth-dir path.
func SetPersistencePath(authDir string) error {
	if authDir == "" {
		return fmt.Errorf("auth directory is empty")
	}

	absPath, err := filepath.Abs(authDir)
	if err != nil {
		return fmt.Errorf("failed to resolve auth directory: %w", err)
	}

	if err := os.MkdirAll(absPath, 0o700); err != nil {
		return fmt.Errorf("failed to create auth directory: %w", err)
	}

	persistenceMu.Lock()
	persistencePath = filepath.Join(absPath, usageStatsFileName)
	persistenceMu.Unlock()

	return nil
}

// GetPersistencePath returns the current persistence file path.
func GetPersistencePath() string {
	persistenceMu.Lock()
	defer persistenceMu.Unlock()
	return persistencePath
}

// LoadFromFile loads usage statistics from the JSON file.
// Should be called during server startup after SetPersistencePath.
func LoadFromFile() error {
	persistenceMu.Lock()
	path := persistencePath
	persistenceMu.Unlock()

	if path == "" {
		return fmt.Errorf("persistence path not set")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			log.Debugf("Usage stats file does not exist, starting fresh: %s", path)
			return nil
		}
		return fmt.Errorf("failed to read usage stats file: %w", err)
	}

	var persisted persistedData
	if err := json.Unmarshal(data, &persisted); err != nil {
		return fmt.Errorf("failed to parse usage stats file: %w", err)
	}

	stats := GetRequestStatistics()
	stats.mu.Lock()
	defer stats.mu.Unlock()

	stats.totalRequests = persisted.TotalRequests
	stats.successCount = persisted.SuccessCount
	stats.failureCount = persisted.FailureCount
	stats.totalTokens = persisted.TotalTokens

	if persisted.APIs != nil {
		for apiName, pAPI := range persisted.APIs {
			api := &apiStats{
				TotalRequests: pAPI.TotalRequests,
				TotalTokens:   pAPI.TotalTokens,
				Models:        make(map[string]*modelStats),
			}
			if pAPI.Models != nil {
				for modelName, pModel := range pAPI.Models {
					api.Models[modelName] = &modelStats{
						TotalRequests: pModel.TotalRequests,
						TotalTokens:   pModel.TotalTokens,
						Details:       pModel.Details,
					}
				}
			}
			stats.apis[apiName] = api
		}
	}

	if persisted.RequestsByDay != nil {
		for k, v := range persisted.RequestsByDay {
			stats.requestsByDay[k] = v
		}
	}
	if persisted.RequestsByHour != nil {
		for k, v := range persisted.RequestsByHour {
			stats.requestsByHour[k] = v
		}
	}
	if persisted.TokensByDay != nil {
		for k, v := range persisted.TokensByDay {
			stats.tokensByDay[k] = v
		}
	}
	if persisted.TokensByHour != nil {
		for k, v := range persisted.TokensByHour {
			stats.tokensByHour[k] = v
		}
	}

	log.Infof("Loaded usage statistics from %s (total requests: %d)", path, stats.totalRequests)
	return nil
}

// SaveToFile persists the current usage statistics to the JSON file.
func SaveToFile() error {
	persistenceMu.Lock()
	path := persistencePath
	persistenceMu.Unlock()

	if path == "" {
		return fmt.Errorf("persistence path not set")
	}

	stats := GetRequestStatistics()
	stats.mu.RLock()

	persisted := persistedData{
		TotalRequests:  stats.totalRequests,
		SuccessCount:   stats.successCount,
		FailureCount:   stats.failureCount,
		TotalTokens:    stats.totalTokens,
		APIs:           make(map[string]*persistedAPI),
		RequestsByDay:  make(map[string]int64),
		RequestsByHour: make(map[int]int64),
		TokensByDay:    make(map[string]int64),
		TokensByHour:   make(map[int]int64),
		LastSaved:      time.Now(),
	}

	for apiName, api := range stats.apis {
		pAPI := &persistedAPI{
			TotalRequests: api.TotalRequests,
			TotalTokens:   api.TotalTokens,
			Models:        make(map[string]*persistedModel),
		}
		for modelName, model := range api.Models {
			pAPI.Models[modelName] = &persistedModel{
				TotalRequests: model.TotalRequests,
				TotalTokens:   model.TotalTokens,
				Details:       model.Details,
			}
		}
		persisted.APIs[apiName] = pAPI
	}

	for k, v := range stats.requestsByDay {
		persisted.RequestsByDay[k] = v
	}
	for k, v := range stats.requestsByHour {
		persisted.RequestsByHour[k] = v
	}
	for k, v := range stats.tokensByDay {
		persisted.TokensByDay[k] = v
	}
	for k, v := range stats.tokensByHour {
		persisted.TokensByHour[k] = v
	}

	stats.mu.RUnlock()

	data, err := json.MarshalIndent(persisted, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal usage stats: %w", err)
	}

	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o600); err != nil {
		return fmt.Errorf("failed to write usage stats temp file: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to rename usage stats file: %w", err)
	}

	log.Debugf("Saved usage statistics to %s", path)
	return nil
}

// StartAutoSave begins a background goroutine that saves statistics periodically.
func StartAutoSave() {
	persistenceMu.Lock()
	if autoSaveRunning {
		persistenceMu.Unlock()
		return
	}
	stopAutoSave = make(chan struct{})
	autoSaveRunning = true
	persistenceMu.Unlock()

	go func() {
		ticker := time.NewTicker(autoSaveInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				if err := SaveToFile(); err != nil {
					log.Warnf("Auto-save usage stats failed: %v", err)
				}
			case <-stopAutoSave:
				return
			}
		}
	}()

	log.Info("Usage statistics auto-save started (interval: 5 minutes)")
}

// StopAutoSave stops the background auto-save goroutine and performs a final save.
func StopAutoSave() {
	persistenceMu.Lock()
	if !autoSaveRunning {
		persistenceMu.Unlock()
		return
	}
	close(stopAutoSave)
	autoSaveRunning = false
	persistenceMu.Unlock()

	if err := SaveToFile(); err != nil {
		log.Warnf("Final usage stats save failed: %v", err)
	} else {
		log.Info("Usage statistics saved on shutdown")
	}
}
