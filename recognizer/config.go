// Файл с загрузчиком конфига. Тут ничего интересного: просто чтение файла
// конфига и парсинг содержимого в Go-структуру.
package main

import (
	"fmt"
	"io/ioutil"

	"gopkg.in/yaml.v2"
)

type Config struct {
	RecognizerModelPath string   `yaml:"recognizer_model_path"`
	ShaperModelPath     string   `yaml:"shaper_model_path"`
	Padding             float64  `yaml:"padding"`
	Jittering           int      `yaml:"jittering"`
	CudaDevice          int      `yaml:"cuda_device"`
	Cameras             []string `yaml:"cameras"`
	NatsURL             string   `yaml:"nats_url"`
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
