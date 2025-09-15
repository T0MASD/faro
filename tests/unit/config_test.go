package unit

import (
	"path/filepath"
	"testing"

	faro "github.com/T0MASD/faro/pkg"
)

func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name        string
		config      faro.Config
		expectError bool
	}{
		{
			name: "valid config",
			config: faro.Config{
				OutputDir: "/tmp/test",
				LogLevel:  "info",
			},
			expectError: false,
		},
		{
			name: "invalid log level",
			config: faro.Config{
				OutputDir: "/tmp/test",
				LogLevel:  "invalid",
			},
			expectError: true,
		},
		{
			name: "empty output dir",
			config: faro.Config{
				OutputDir: "",
				LogLevel:  "info",
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if tt.expectError && err == nil {
				t.Errorf("expected error but got none")
			}
			if !tt.expectError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestConfigNormalization(t *testing.T) {
	config := &faro.Config{
		OutputDir: "/tmp/test",
		LogLevel:  "info",
		Resources: []faro.ResourceConfig{
			{
				GVR:               "v1/configmaps",
				Scope:             faro.NamespaceScope,
		NamespaceNames: []string{"test-namespace"},
		NameSelector:   "test-config",
				LabelSelector:     "app=test",
			},
		},
	}

	normalized, err := config.Normalize()
	if err != nil {
		t.Fatalf("normalization failed: %v", err)
	}

	if len(normalized) != 1 {
		t.Errorf("expected 1 normalized config, got %d", len(normalized))
	}

	if configs, exists := normalized["v1/configmaps"]; !exists {
		t.Error("expected v1/configmaps in normalized config")
	} else if len(configs) != 1 {
		t.Errorf("expected 1 config for v1/configmaps, got %d", len(configs))
	} else {
		config := configs[0]
	if config.NameSelector != "test-config" {
		t.Errorf("expected NameSelector 'test-config', got '%s'", config.NameSelector)
		}
		if config.LabelSelector != "app=test" {
			t.Errorf("expected LabelSelector 'app=test', got '%s'", config.LabelSelector)
		}
	}
}

func TestGetLogLevel(t *testing.T) {
	tests := []struct {
		logLevel string
		expected int
	}{
		{"debug", -1},
		{"info", 0},
		{"warning", 1},
		{"error", 2},
		{"fatal", 3},
		{"invalid", 0}, // defaults to info
	}

	for _, tt := range tests {
		t.Run(tt.logLevel, func(t *testing.T) {
			config := &faro.Config{LogLevel: tt.logLevel}
			result := config.GetLogLevel()
			if result != tt.expected {
				t.Errorf("expected %d, got %d", tt.expected, result)
			}
		})
	}
}

func TestGetLogDir(t *testing.T) {
	config := &faro.Config{OutputDir: "/tmp/test"}
	expected := filepath.Join("/tmp/test", "logs")
	result := config.GetLogDir()
	if result != expected {
		t.Errorf("expected %s, got %s", expected, result)
	}
}