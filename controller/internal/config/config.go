package config

import (
	"fmt"
	"os"
	"sync"

	"gopkg.in/yaml.v3"
)

type ServerConfig struct {
	HTTPPort          int `yaml:"http_port" json:"http_port"`
	DefaultStreamPort int `yaml:"default_stream_port" json:"default_stream_port"`
	WSPingIntervalSec int `yaml:"ws_ping_interval_sec" json:"ws_ping_interval_sec"`
}

type CanvasConfig struct {
	Width  int `yaml:"width" json:"width"`
	Height int `yaml:"height" json:"height"`
	FPS    int `yaml:"fps" json:"fps"`
}

type DisplayConfig struct {
	ID                string `yaml:"id" json:"id"`
	Name              string `yaml:"name" json:"name"`
	IP                string `yaml:"ip" json:"ip"`
	Port              int    `yaml:"port" json:"port"`
	Width             int    `yaml:"width" json:"width"`
	Height            int    `yaml:"height" json:"height"`
	CropX             int    `yaml:"crop_x" json:"crop_x"`
	CropY             int    `yaml:"crop_y" json:"crop_y"`
	BezelPaddingRight int    `yaml:"bezel_padding_right" json:"bezel_padding_right"`
}

type MediaConfig struct {
	DefaultMode string `yaml:"default_mode" json:"default_mode"` // "testpattern" or "video"
	VideoFile   string `yaml:"video_file" json:"video_file"`
}

type PipelineConfig struct {
	Encoder     string `yaml:"encoder" json:"encoder"`           // "auto", "vaapi", "software"
	VAAPIDevice string `yaml:"vaapi_device" json:"vaapi_device"` // default: "/dev/dri/renderD128"
}

type Config struct {
	Server   ServerConfig    `yaml:"server" json:"server"`
	Canvas   CanvasConfig    `yaml:"canvas" json:"canvas"`
	Displays []DisplayConfig `yaml:"displays" json:"displays"`
	Media    MediaConfig     `yaml:"media" json:"media"`
	Pipeline PipelineConfig  `yaml:"pipeline" json:"pipeline"`

	filePath string       `yaml:"-" json:"-"`
	mu       sync.RWMutex `yaml:"-" json:"-"`
}

// LoadConfig reads and parses the YAML config from path.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	cfg := &Config{filePath: path}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse yaml config: %w", err)
	}

	// Defaults if missing
	if cfg.Server.HTTPPort == 0 {
		cfg.Server.HTTPPort = 8080
	}
	if cfg.Server.DefaultStreamPort == 0 {
		cfg.Server.DefaultStreamPort = 8554
	}
	if cfg.Canvas.FPS == 0 {
		cfg.Canvas.FPS = 30
	}
	if cfg.Media.DefaultMode == "" {
		cfg.Media.DefaultMode = "testpattern"
	}
	if cfg.Pipeline.Encoder == "" {
		cfg.Pipeline.Encoder = "auto"
	}
	if cfg.Pipeline.VAAPIDevice == "" {
		cfg.Pipeline.VAAPIDevice = "/dev/dri/renderD128"
	}

	return cfg, nil
}

// GetSnapshot returns a thread-safe copy of the configuration.
func (c *Config) GetSnapshot() Config {
	c.mu.RLock()
	defer c.mu.RUnlock()

	displays := make([]DisplayConfig, len(c.Displays))
	copy(displays, c.Displays)

	return Config{
		Server:   c.Server,
		Canvas:   c.Canvas,
		Displays: displays,
		Media:    c.Media,
		Pipeline: c.Pipeline,
		filePath: c.filePath,
	}
}

// Save persists the current configuration back to disk.
func (c *Config) Save() error {
	c.mu.RLock()
	data, err := yaml.Marshal(c)
	filePath := c.filePath
	c.mu.RUnlock()

	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}
	if filePath == "" {
		return fmt.Errorf("no file path configured for config")
	}

	return os.WriteFile(filePath, data, 0644)
}

// UpdateDisplays updates the displays layout safely.
func (c *Config) UpdateDisplays(displays []DisplayConfig) error {
	c.mu.Lock()
	c.Displays = displays
	c.mu.Unlock()
	return c.Save()
}

// UpdateMedia updates media playback configuration.
func (c *Config) UpdateMedia(mode, videoFile string) error {
	c.mu.Lock()
	if mode != "" {
		c.Media.DefaultMode = mode
	}
	if videoFile != "" {
		c.Media.VideoFile = videoFile
	}
	c.mu.Unlock()
	return c.Save()
}
