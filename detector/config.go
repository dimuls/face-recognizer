package main

import (
	"fmt"
	"io/ioutil"
	"time"

	"gopkg.in/yaml.v2"
)

type CameraConfig struct {
	ID  string `yaml:"id"`
	URL string `yaml:"url"`
}

type Config struct {
	ModelPath      string         `yaml:"model_path"`
	CudaDeviceID   int            `yaml:"cuda_device_id"`
	FrameRate      int            `yaml:"frame_rate"`
	ClosedDuration time.Duration  `yaml:"closed_duration"`
	Cameras        []CameraConfig `yaml:"camera"`
	NatsURL        string         `yaml:"nats_url"`
}

type configYAML struct {
	ClosedDuration string `yaml:"batch_wait"`
	Config
}

func (c *Config) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var cYAML configYAML

	err := unmarshal(&cYAML)
	if err != nil {
		return fmt.Errorf("parse config: %w", err)
	}

	*c = cYAML.Config

	c.ClosedDuration, err = time.ParseDuration(cYAML.ClosedDuration)
	if err != nil {
		return fmt.Errorf("parse duration: %w", err)
	}

	return nil
}

func LoadConfig(configPath string) (Config, error) {
	configYAML, err := ioutil.ReadFile(configPath)
	if err != nil {
		return Config{}, fmt.Errorf("load config: %w", err)
	}

	var config Config

	err = yaml.Unmarshal(configYAML, &config)
	if err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}

	return config, nil
}
