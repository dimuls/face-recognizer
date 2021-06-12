package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/dimuls/face"
	natsGo "github.com/nats-io/nats.go"
	"github.com/sirupsen/logrus"
	"gocv.io/x/gocv"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/dimuls/face-recognizer/entity"
	"github.com/dimuls/face-recognizer/nats"
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

	// Установка CUDA-устройства.
	err = os.Setenv("CUDA_VISIBLE_DEVICES", strconv.Itoa(config.CudaDevice))
	if err != nil {
		logrus.WithError(err).Fatal("failed to set cuda device id")
	}

	// Если в конфиге не указана частота кадров, то выставляем стандартные 25.
	if config.FrameRate == 0 {
		config.FrameRate = 25
	}

	// Если в конфиге нет камер, то выходим с ошибкой.
	if len(config.Cameras) == 0 {
		logrus.Panic("no camera defined")
	}

	// Создание детектора.
	detector, err := face.NewDetector(config.ModelPath)
	if err != nil {
		logrus.WithError(err).Fatal("failed to create detector")
	}

	logrus.Info("detector created")

	// Подключение к nats.
	natsConn, err := natsGo.Connect(config.NatsURL)
	if err != nil {
		logrus.WithError(err).Fatal("failed to connect to nats")
	}

	logrus.Info("connected to nats")

	// Подготовка каналов для получения кадров из обработчика камер.
	frames := map[string]<-chan []byte{}

	// Контекст для остановки приложения.
	ctx, cancel := context.WithCancel(context.Background())

	// Группа для ожидания завершения обработчиков.
	var wg sync.WaitGroup

	// Запуск обработчиков кадров.
	for _, camera := range config.Cameras {
		frames[camera.ID] = runCameraProcessor(ctx, &wg, camera,
			config.ClosedDuration)
	}

	// Запуск детектора.
	runDetector(ctx, &wg, detector, natsConn, config.FrameRate, frames)

	logrus.Info("everything is started")

	// Ожидания сигнала завершения.
	exit := make(chan os.Signal)
	signal.Notify(exit, os.Interrupt, syscall.SIGTERM)
	<-exit

	logrus.Info("exit signal received, stopping")

	// Остановка обработчиков.
	cancel()

	// Ожидания завершения всех обработчиков.
	wg.Wait()

	logrus.Info("everything is stopped, exiting")
}

// Обработчик камер: подключается к камере, получает кадры, кодирует их в
// JPEG и отправляет в детектор. Кодирование в JPEG необходимо для
// предотвращения утечки памяти: при передачи gocv.Mat между горутинами, которые
// работают в разных ОС-потоках, происходит утечка памяти.
func runCameraProcessor(ctx context.Context, wg *sync.WaitGroup,
	camera CameraConfig, closedDuration time.Duration) <-chan []byte {

	// Канал JPEG-кадров с камеры.
	frames := make(chan []byte)

	// Запуск горутины обработчика камеры.
	wg.Add(1)
	go func() {
		defer wg.Done()

		log := logrus.WithFields(logrus.Fields{
			"subsystem": "camera_processor",
			"camera_id": camera.ID,
		})

		log.Info("subsystem started")
		defer log.Info("subsystem stopped")

		// Фиксируем ОС-поток для предотвращения утечек.
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		var (
			err    error
			stream *gocv.VideoCapture

			successTime = time.Now()
			readTries   int

			image = gocv.NewMat()
		)

		// Основной цикл обработки.
	loop:
		for {
			// Выход из обработчика, если требуется.
			select {
			case <-ctx.Done():
				break loop
			default:
			}

			// Если видеопоток отсутствует, то мы его создаём.
			if stream == nil {
				stream, err = gocv.OpenVideoCapture(camera.URL)
				if err != nil {
					log.WithError(err).Error("failed to open stream")
					time.Sleep(time.Second)
					continue
				}
			}

			// Если были ошибки чтения и мы слишком давно не получали кадров,
			// то что-то не так с видеопотоком. Мы его закрываем и обнуляем.
			// Далее в условии выше мы его снова создадим.
			if readTries > 0 && time.Now().Sub(successTime) > closedDuration {
				log.Warning("failed to read too long, reconnecting")
				err = stream.Close()
				if err != nil {
					log.WithError(err).Error("failed to close stream")
				}
				stream = nil
				successTime = time.Now()
				readTries = 0
				continue
			}

			// Читаем очередной кадр, если не удалось, то ждём немного,
			// обновляем статистику и начинаем цикл заново.
			if !stream.Read(&image) {
				readTries++
				time.Sleep(time.Second)
				continue
			}

			// Кодируем кадр в JPEG.
			frame, err := gocv.IMEncode(gocv.JPEGFileExt, image)
			if err != nil {
				log.WithError(err).Error("failed to JPEG encode image")
				continue
			}

			// Неблокирующая отправка кадра по каналу в детектор. Если
			// канал никто не читает на момент записи, то кадр теряется.
			select {
			case frames <- frame:
			default:
			}

			// Кадр успешно отправился, обновляем статистику.
			successTime = time.Now()
			readTries = 0
		}

		// Закрываем видеопоток перед выходом.
		if stream != nil {
			err = stream.Close()
			if err != nil {
				log.WithError(err).Error("failed to close stream")
			}
		}
	}()

	return frames
}

// Обработчик детектора лиц.
func runDetector(ctx context.Context, wg *sync.WaitGroup,
	detector *face.Detector, natsConn *natsGo.Conn,
	frameRate int, cameraFramesChans map[string]<-chan []byte) {

	// Подготовка данных для хранения текущих кадров.
	var (
		cameraFramesMap   = make(map[string][]byte, len(cameraFramesChans))
		cameraFramesMapMx sync.Mutex
	)

	// Запуск сборщиков кадров: из обработчика камер к нам будут поступать
	// новые кадры, которые нужно получить и собрать в cameraFramesMap.
	// Нам это нужно, чтобы далее, в обработчике детектора, запустить пакетное
	// обнаружение лиц сразу по всем собранным кадрам.
	for cameraID, cameraFramesChan := range cameraFramesChans {
		wg.Add(1)
		go func(cameraID string, cameraFramesChan <-chan []byte) {
			defer wg.Done()

			log := logrus.WithFields(logrus.Fields{
				"subsystem": "detector_frame_collector",
				"camera_id": cameraID,
			})

			log.Info("subsystem started")
			defer log.Info("subsystem stopped")

			var frame []byte

			for {
				select {
				case <-ctx.Done():
					return
				case frame = <-cameraFramesChan:
				}
				cameraFramesMapMx.Lock()
				cameraFramesMap[cameraID] = frame
				cameraFramesMapMx.Unlock()
			}

		}(cameraID, cameraFramesChan)
	}

	// Запуск детектора.
	wg.Add(1)
	go func() {
		defer wg.Done()

		// Фиксируем ОС-поток для предотвращение утечек.
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		log := logrus.WithFields(logrus.Fields{
			"subsystem": "detector",
		})

		log.Info("subsystem started")
		defer log.Info("subsystem stopped")

		// Подготовка данных детектора.
		var (
			currentCameraFramesMap map[string][]byte

			frames       = make([][]byte, 0, len(cameraFramesChans))
			images       []gocv.Mat
			imageIndexes = make(map[string]int, len(cameraFramesChans))

			frameTime = time.Second / time.Duration(frameRate)
			startTime time.Time
		)

		// Таймер для ожидания очередных кадров. Используется если
		// с момента последнего кадра прошло менее чем 1 сек / частоту кадров.
		t := time.NewTimer(frameTime)
		defer t.Stop()

		for {
			// Ожидание следующих кадров либо моментальный переход к их
			// обработке.
			if time.Now().Sub(startTime) < frameTime {
				if !t.Stop() {
					select {
					case <-t.C:
					default:
					}
				}
				t.Reset(time.Now().Sub(startTime))
				select {
				case <-ctx.Done():
					return
				case <-t.C:
				}
			} else {
				select {
				case <-ctx.Done():
					return
				default:
				}
			}

			startTime = time.Now()

			// Получаем очередные кадры.
			cameraFramesMapMx.Lock()
			currentCameraFramesMap = cameraFramesMap
			cameraFramesMap = make(map[string][]byte, len(cameraFramesChans))
			cameraFramesMapMx.Unlock()

			// Проходимся по всем кадрам, декодируем JPEG-кадры в картинки
			// gocv.Mat и собираем дополнительные данные.
			for cameraID, frame := range currentCameraFramesMap {
				image, err := gocv.IMDecode(frame, gocv.IMReadUnchanged)
				if err != nil {
					log.WithError(err).Error("failed to decode frame image")
					continue
				}
				frames = append(frames, frame)
				images = append(images, image)
				imageIndexes[cameraID] = len(images) - 1
			}

			// Если нет кадров, то переходим к следующему циклу.
			if len(frames) == 0 {
				continue
			}

			// Пакетное обнаружение лиц.
			detections, err := detector.BatchDetect(images)
			if err != nil {
				log.WithError(err).Error("failed to batch detect")
			}

			detectedAt := time.Now()

			// Проходимся по всем камерам.
			for cameraID, i := range imageIndexes {

				// Собираем все обнаружения лиц в кадре из очередной камеры.
				var ds []*entity.Detection
				for _, detection := range detections[i] {
					ds = append(ds, &entity.Detection{
						MinX:       int32(detection.Rectangle.Min.X),
						MinY:       int32(detection.Rectangle.Min.Y),
						MaxX:       int32(detection.Rectangle.Max.X),
						MaxY:       int32(detection.Rectangle.Max.Y),
						Confidence: detection.Confidence,
					})
				}

				// Если обнаружений нет, то пропускаем камеру.
				if len(ds) == 0 {
					continue
				}

				// Кодируем protobuf-структуру с данными обнаруженных лиц.
				dsProto, err := proto.Marshal(&entity.Detections{
					CameraId:   cameraID,
					Frame:      frames[i],
					Detections: ds,
					DetectedAt: timestamppb.New(detectedAt),
				})
				if err != nil {
					log.WithError(err).Error(
						"failed to proto marshal detections")
					continue
				}

				// Отправляем полученную protobuf-структуру в соответствующие
				// камере каналу в nats.
				err = natsConn.Publish(nats.CameraDetectionsSubject(cameraID),
					dsProto)
				if err != nil {
					log.WithError(err).Error(
						"failed to publish detections")
				}
			}

			// Очистка данных.

			frames = frames[:0]

			for _, image := range images {
				err := image.Close()
				if err != nil {
					log.WithError(err).Error("failed to close frame image")
				}
			}

			images = images[:0]

			for cameraID := range imageIndexes {
				delete(imageIndexes, cameraID)
			}
		}
	}()
}
