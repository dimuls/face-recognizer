package main

import (
	"context"
	"flag"
	"image"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"sync"
	"syscall"

	"github.com/dimuls/face"
	"github.com/dimuls/face-recognizer/entity"
	"github.com/dimuls/face-recognizer/nats"
	natsGo "github.com/nats-io/nats.go"
	"github.com/sirupsen/logrus"
	"gocv.io/x/gocv"
	"google.golang.org/protobuf/proto"
)

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

	if config.Padding < 0 {
		logrus.Fatal("invalid padding")
	}

	if config.Jittering < 0 {
		logrus.Fatal("invalid jittering")
	}

	// Установка CUDA-устройства.
	err = os.Setenv("CUDA_VISIBLE_DEVICES", strconv.Itoa(config.CudaDevice))
	if err != nil {
		logrus.WithError(err).Fatal("failed to set cuda device id")
	}

	// Если в конфиге нет камер, то выходим с ошибкой.
	if len(config.Cameras) == 0 {
		logrus.Panic("no camera defined")
	}

	// Создание распознавателя.
	recognizer, err := face.NewRecognizer(config.ShaperModelPath,
		config.RecognizerModelPath)
	if err != nil {
		logrus.WithError(err).Fatal("failed to create detector")
	}

	logrus.Info("recognizer created")

	// Подключение к nats.
	natsConn, err := natsGo.Connect(config.NatsURL)
	if err != nil {
		logrus.WithError(err).Fatal("failed to connect to nats")
	}

	logrus.Info("connected to nats")

	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(context.Background())

	// Для каждой камеры создаём горутину-обработчик, которая будет
	// подписываться на соответствующий канал nats, получать сообщения с
	// обнаружениями, распознавать лица, и публиковать распознавания в nats.
	for _, cameraID := range config.Cameras {
		wg.Add(1)
		go func(cameraID string) {
			defer wg.Done()

			// Фиксируем ОС-поток для предотвращение утечек.
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()

			log := logrus.WithFields(logrus.Fields{
				"subsystem": "recognizer",
				"camera_id": cameraID,
			})

			// Подписка на канал камеры.
			sub, err := natsConn.SubscribeSync(
				nats.CameraDetectionsSubject(cameraID))
			if err != nil {
				log.WithError(err).Error("failed to subscribe to detections")
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

				ds := &entity.Detections{}

				// Декодируем обнаружения.
				err = proto.Unmarshal(msg.Data, ds)
				if err != nil {
					logrus.WithError(err).Error("failed to proto unmarshal detections")
					return
				}

				// Декодируем изображения кадра.
				img, err := gocv.IMDecode(ds.Frame, gocv.IMReadUnchanged)
				if err != nil {
					logrus.WithError(err).Error(
						"failed to JPEG decode frame")
					continue
				}

				// Для каждого обнаружения.
				for _, d := range ds.Detections {

					// Получаем квадрат лица.
					faceRect := image.Rect(int(d.MinX), int(d.MinY),
						int(d.MaxX), int(d.MaxY))

					// Распознаём (векторизуем) лицо.
					descr, err := recognizer.Recognize(img, faceRect,
						config.Padding, config.Jittering)
					if err != nil {
						logrus.WithError(err).Error(
							"failed to recognize face")
						continue
					}

					// Вырезаем изображения лица, кодируем его.
					face, err := gocv.IMEncode(gocv.JPEGFileExt,
						img.Region(faceRect))
					if err != nil {
						logrus.WithError(err).Errorf(
							"failed to JPEG encode face image")
						continue
					}

					// Формируем и кодируем распознавание.
					recProto, err := proto.Marshal(&entity.Recognition{
						CameraId:       cameraID,
						Face:           face,
						FaceDescriptor: descr[:],
						DetectedAt:     ds.DetectedAt,
					})

					// Публикуем распознавание в nats.
					err = natsConn.Publish(
						nats.CameraRecognitionsSubject(cameraID), recProto)
					if err != nil {
						logrus.WithError(err).Errorf(
							"failed to publish recognition to nats")
					}
				}

				// Очистка данных.

				err = img.Close()
				if err != nil {
					logrus.WithError(err).Error("failed to close image")
				}
			}

		}(cameraID)
	}

	logrus.Info("recognizer started")

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

	logrus.Info("everything is stopped, exiting")
}
