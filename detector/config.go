// Файл с загрузчиком конфига. Тут ничего интересного: просто чтение файла
// конфига и парсинг содержимого в Go-структуру.
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
	configYAML
	ClosedDuration time.Duration `yaml:"closed_duration"`
}

type configYAML struct {
	ModelPath      string         `yaml:"model_path"`
	CudaDevice     int            `yaml:"cuda_device"`
	FrameRate      int            `yaml:"frame_rate"`
	ClosedDuration string         `yaml:"closed_duration"`
	Cameras        []CameraConfig `yaml:"cameras"`
	NatsURL        string         `yaml:"nats_url"`
}

func (c *Config) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var cYAML configYAML

	err := unmarshal(&cYAML)
	if err != nil {
		return fmt.Errorf("parse config: %w", err)
	}

	c.configYAML = cYAML

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
