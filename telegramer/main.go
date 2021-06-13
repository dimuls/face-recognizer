package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"math"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/dimuls/face"
	_ "github.com/lib/pq"
	natsGo "github.com/nats-io/nats.go"
	"github.com/sirupsen/logrus"
	"google.golang.org/protobuf/proto"
	"gopkg.in/tucnak/telebot.v2"

	"github.com/dimuls/face-recognizer/entity"
	"github.com/dimuls/face-recognizer/nats"
)

type Face struct {
	PersonID   int64
	Descriptor face.Descriptor
}

func EuclideanDistance(d1, d2 face.Descriptor) float64 {
	var sum float64
	for i := range d1 {
		sum += math.Pow(float64(d1[i])-float64(d2[i]), 2)
	}
	return math.Sqrt(sum)
}

func FindClosestFace(faces []Face, d face.Descriptor) (Face, float64) {
	closestFace := faces[0]
	closestDistance := EuclideanDistance(faces[0].Descriptor, d)
	for i := 1; i < len(faces); i++ {
		d := EuclideanDistance(faces[i].Descriptor, d)
		if d < closestDistance {
			closestFace = faces[i]
			closestDistance = d
		}
	}
	return closestFace, closestDistance
}

func main() {
	logrus.SetLevel(logrus.DebugLevel)

	// Парсинг флагов.
	var configPath string

	flag.StringVar(&configPath, "conf", "config.yaml", "config path")
	flag.Parse()

	// Загрузка конфига.
	config, err := LoadConfig(configPath)
	if err != nil {
		logrus.WithError(err).Fatal("failed to load config")
	}

	logrus.Info("config loaded")

	// Подключение к nats.
	natsConn, err := natsGo.Connect(config.NatsURL)
	if err != nil {
		logrus.WithError(err).Fatal("failed to connect to nats")
	}

	// Создаём телеграм бота.
	b, err := telebot.NewBot(telebot.Settings{
		Token: config.TelegramBotToken,
	})
	if err != nil {
		logrus.WithError(err).Fatal("failed to create telegram bot")
	}

	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(context.Background())

	// Запуск обработчика telegramer.
	wg.Add(1)
	go func() {
		defer wg.Done()

		log := logrus.WithFields(logrus.Fields{
			"subsystem": "telegramer",
			"camera_id": config.Camera,
		})

		// Подписка на канал камеры.
		sub, err := natsConn.SubscribeSync(
			nats.CameraAlertsSubject(config.Camera))
		if err != nil {
			log.WithError(err).Error(
				"failed to subscribe to recognitions")
			cancel()
			return
		}

		// Основной цикл обработки.
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			// Получаем очередное сообщение.
			msg, err := sub.NextMsgWithContext(ctx)
			if err != nil {
				continue
			}

			a := &entity.Alert{}

			// Декодируем обнаружения.
			err = proto.Unmarshal(msg.Data, a)
			if err != nil {
				logrus.WithError(err).Error(
					"failed to proto unmarshal recognition")
				return
			}

			// Формируем телеграм сообщение с фотографией обнаруженного лица.
			f := &telebot.Photo{
				File: telebot.FromReader(bytes.NewReader(a.Face)),
				Caption: fmt.Sprintf("Имя: %s, Камера: %s",
					a.Name, a.CameraId),
			}

			// Рассылка по телеграм-чатам сообщения.
			for _, chatID := range config.Chats {
				b.Send(telebot.ChatID(chatID), f)
			}
		}
	}()

	logrus.Info("telegramer started")

	exit := make(chan os.Signal)
	signal.Notify(exit, os.Interrupt, syscall.SIGTERM)

	// Ожидания сигнала завершения или аварийного завершения.
	select {
	case <-exit:
	case <-ctx.Done():
	}

	logrus.Info("exit signal received, stopping")

	// Остановка обработчиков.
	cancel()

	// Ожидания завершения всех обработчиков.
	wg.Wait()

	b.Stop()

	logrus.Info("everything is stopped, exiting")
}
