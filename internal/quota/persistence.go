package quota

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
	quotaUsageFileName = "quota_usage.json"
	autoSaveInterval   = 1 * time.Minute
)

var (
	persistenceMu   sync.Mutex
	persistencePath string
	stopAutoSave    chan struct{}
	autoSaveRunning bool
)

// persistedUsageData is the JSON structure saved to disk.
type persistedUsageData struct {
	Usage     map[string]*QuotaUsage `json:"usage"`
	LastSaved time.Time              `json:"last_saved"`
	Version   int                    `json:"version"`
}

// SetPersistencePath configures the directory where quota_usage.json will be stored.
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
	persistencePath = filepath.Join(absPath, quotaUsageFileName)
	persistenceMu.Unlock()

	return nil
}

// GetPersistencePath returns the current persistence file path.
func GetPersistencePath() string {
	persistenceMu.Lock()
	defer persistenceMu.Unlock()
	return persistencePath
}

// LoadUsageFromFile loads quota usage from the JSON file.
func LoadUsageFromFile() error {
	persistenceMu.Lock()
	path := persistencePath
	persistenceMu.Unlock()

	if path == "" {
		return fmt.Errorf("persistence path not set")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			log.Debugf("Quota usage file does not exist, starting fresh: %s", path)
			return nil
		}
		return fmt.Errorf("failed to read quota usage file: %w", err)
	}

	var persisted persistedUsageData
	if err := json.Unmarshal(data, &persisted); err != nil {
		return fmt.Errorf("failed to parse quota usage file: %w", err)
	}

	manager := GetManager()
	for apiKey, usage := range persisted.Usage {
		manager.SetUsage(apiKey, usage)
	}

	log.Infof("Loaded quota usage for %d API keys from %s", len(persisted.Usage), path)
	return nil
}

// SaveUsageToFile persists the current quota usage to the JSON file.
func SaveUsageToFile() error {
	persistenceMu.Lock()
	path := persistencePath
	persistenceMu.Unlock()

	if path == "" {
		return fmt.Errorf("persistence path not set")
	}

	manager := GetManager()
	allUsage := manager.AllUsage()

	persisted := persistedUsageData{
		Usage:     allUsage,
		LastSaved: time.Now(),
		Version:   1,
	}

	data, err := json.MarshalIndent(persisted, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal quota usage: %w", err)
	}

	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o600); err != nil {
		return fmt.Errorf("failed to write quota usage temp file: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to rename quota usage file: %w", err)
	}

	log.Debugf("Saved quota usage for %d API keys to %s", len(allUsage), path)
	return nil
}

// StartAutoSave begins a background goroutine that saves usage periodically.
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
				if err := SaveUsageToFile(); err != nil {
					log.Warnf("Auto-save quota usage failed: %v", err)
				}
			case <-stopAutoSave:
				return
			}
		}
	}()

	log.Info("Quota usage auto-save started (interval: 1 minute)")
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

	if err := SaveUsageToFile(); err != nil {
		log.Warnf("Final quota usage save failed: %v", err)
	} else {
		log.Info("Quota usage saved on shutdown")
	}
}

// Initialize sets up the quota system with the given auth directory.
func Initialize(authDir string) error {
	// Set persistence path
	if err := SetPersistencePath(authDir); err != nil {
		return fmt.Errorf("failed to set persistence path: %w", err)
	}

	// Initialize default pricing
	InitDefaultPricing()

	// Load existing usage data
	if err := LoadUsageFromFile(); err != nil {
		log.Warnf("Failed to load quota usage: %v", err)
	}

	// Start auto-save
	StartAutoSave()

	return nil
}

// Shutdown gracefully shuts down the quota system.
func Shutdown() {
	StopAutoSave()
}
