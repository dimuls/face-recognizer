// Файл с загрузчиком конфига. Тут ничего интересного: просто чтение файла
// конфига и парсинг содержимого в Go-структуру.
package main

import (
	"fmt"
	"io/ioutil"
	"time"

	"gopkg.in/yaml.v2"
)

type Config struct {
	configYAML
	ActiveFaceDuration      time.Duration
	ActiveFaceCleanerPeriod time.Duration
}

type configYAML struct {
	PostgresDSN             string   `yaml:"postgres_dsn"`
	ActiveFaceDuration      string   `yaml:"active_face_duration"`
	ActiveFaceCleanerPeriod string   `yaml:"active_face_cleaner_period"`
	SimilarFaceDistance     float64  `yaml:"similar_face_distance"`
	Cameras                 []string `yaml:"cameras"`
	NatsURL                 string   `yaml:"nats_url"`
}

func (c *Config) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var cYAML configYAML

	err := unmarshal(&cYAML)
	if err != nil {
		return fmt.Errorf("parse config: %w", err)
	}

	c.configYAML = cYAML

	c.ActiveFaceDuration, err = time.ParseDuration(cYAML.ActiveFaceDuration)
	if err != nil {
		return fmt.Errorf("parse active face duration: %w", err)
	}

	c.ActiveFaceCleanerPeriod, err = time.ParseDuration(cYAML.ActiveFaceCleanerPeriod)
	if err != nil {
		return fmt.Errorf("parse active face cleaner period: %w", err)
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
