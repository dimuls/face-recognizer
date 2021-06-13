// Файл с загрузчиком конфига. Тут ничего интересного: просто чтение файла
// конфига и парсинг содержимого в Go-структуру.
package main

import (
	"fmt"
	"io/ioutil"

	"gopkg.in/yaml.v2"
)

type Config struct {
	TelegramBotToken string  `yaml:"telegram_bot_token"`
	Camera           string  `yaml:"camera"`
	Chats            []int64 `yaml:"chats"`
	NatsURL          string  `yaml:"nats_url"`
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
