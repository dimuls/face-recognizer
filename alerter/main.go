package main

import (
	"context"
	"database/sql"
	"flag"
	"math"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/dimuls/face"
	"github.com/lib/pq"
	natsGo "github.com/nats-io/nats.go"
	"github.com/sirupsen/logrus"
	"google.golang.org/protobuf/proto"

	"github.com/dimuls/face-recognizer/entity"
	"github.com/dimuls/face-recognizer/nats"
)

// Структура лица
type Face struct {
	PersonID   int64
	Descriptor face.Descriptor
}

// Функция расчёта Евклидовго расстояния между двумя дескрипторами лица.
func EuclideanDistance(d1, d2 face.Descriptor) float64 {
	var sum float64
	for i := range d1 {
		sum += math.Pow(float64(d1[i])-float64(d2[i]), 2)
	}
	return math.Sqrt(sum)
}

// Функция для поиска ближайшего к дескриптору лица.
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

	// Устанавливаем минимальное время активного лица. Лицо активно, если
	// из распознавателя пришёл дескриптор, который соответствовал этому лицу,
	// и мы отправили тревогу об этом в nats.
	if config.ActiveFaceDuration < time.Minute {
		config.ActiveFaceDuration = time.Minute
	}

	// Подключение к базе данных.
	db, err := sql.Open("postgres", config.PostgresDSN)
	if err != nil {
		logrus.WithError(err).Fatal("failed to open db")
	}

	// Массив лиц, в которую мы будем загружать лица из базы данных.
	var faces []Face

	// Запрашиваем лица из базы данных.
	rows, err := db.Query(`select person_id, descriptor from face`)
	if err != nil {
		logrus.WithError(err).Fatal("failed to query faces from db")
	}

	// Проходимся по каждой строке, которая вернула база данных, получаем
	// лицо из строки и заносим лицо в массив лиц.
	for rows.Next() {
		var (
			f     Face
			descr []float32
		)
		err = rows.Scan(&f.PersonID, pq.Array(&descr))
		if err != nil {
			logrus.WithError(err).Fatal("failed to scan face from db")
		}
		copy(f.Descriptor[:], descr)
		faces = append(faces, f)
	}
	if err := rows.Err(); err != nil {
		logrus.WithError(err).Fatal("failed to get all faces from db")
	}

	// Подключение к nats.
	natsConn, err := natsGo.Connect(config.NatsURL)
	if err != nil {
		logrus.WithError(err).Fatal("failed to connect to nats")
	}

	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(context.Background())

	// Для каждой камеры в конфиге мы запускаем обработчик, который будет
	// получать распознавания, искать самое похожее лицо в массиве наших
	// лиц, которые мы загрузили из базы данных, и поднимать тревогу с этим
	// лицом если он совпал полученным распознаванием.
	for _, cameraID := range config.Cameras {

		// Кеш активных лиц. Мы храним в памяти лица, о которых мы уже подняли
		// тревогу, храним время когда была поднята тревога, чтобы не спамить
		// тревогами лишний раз.
		activeFaces := map[int64]time.Time{}
		var activeFacesMx sync.RWMutex

		// Запуск обработчика кеша активных лиц. Кеш должен очищаться, чтобы
		// мы не переполнили память.
		wg.Add(1)
		go func() {
			defer wg.Done()

			t := time.NewTicker(config.ActiveFaceCleanerPeriod)
			defer t.Stop()

			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
				}

				activeFacesMx.Lock()
				for k, noticedAt := range activeFaces {
					if time.Now().Sub(noticedAt) > config.ActiveFaceDuration {
						delete(activeFaces, k)
					}
				}
				activeFacesMx.Unlock()
			}
		}()

		// Запуск обработчика тревог камеры.
		wg.Add(1)
		go func(cameraID string) {
			defer wg.Done()

			log := logrus.WithFields(logrus.Fields{
				"subsystem": "alerter",
				"camera_id": cameraID,
			})

			// Подписка на канал распознаваний камеры.
			sub, err := natsConn.SubscribeSync(
				nats.CameraRecognitionsSubject(cameraID))
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

				r := &entity.Recognition{}

				// Декодируем распознавание.
				err = proto.Unmarshal(msg.Data, r)
				if err != nil {
					logrus.WithError(err).Error(
						"failed to proto unmarshal recognition")
					return
				}

				// Формируем дескриптор лица.
				var descriptor face.Descriptor
				copy(descriptor[:], r.FaceDescriptor)

				// Ищем ближайшее дескриптору лицо в нашем массиве лиц.
				face, distance := FindClosestFace(faces, descriptor)

				// Если расстояние между дескриптором и ближайшим лицом
				// больше некоторого заданного порога, то мы пропускаем это
				// распознавание. Мы считаем, что распознанного лица нет в базе
				// данных.
				if distance > config.SimilarFaceDistance {
					continue
				}

				// Достаём время последней тревоги найденного лица.
				activeFacesMx.RLock()
				noticedAt := activeFaces[face.PersonID]
				activeFacesMx.RUnlock()

				// Если это время меньше какого-то порога, то мы пропускаем
				// это распознавание: считаем что тревога уже поднята.
				if time.Now().Sub(noticedAt) <= config.ActiveFaceDuration {
					continue
				}

				// Получаем имя найденного лица из БД.
				var name string
				err = db.QueryRow(`select name from person where id = $1`,
					face.PersonID).Scan(&name)
				if err != nil {
					logrus.WithError(err).Error("failed to get person name")
					continue
				}

				// Формируем тревогу с полученными данными.
				aProto, err := proto.Marshal(&entity.Alert{
					CameraId:   r.CameraId,
					Face:       r.Face,
					Name:       name,
					DetectedAt: r.DetectedAt,
				})

				// Публикуем тревогу в nats.
				err = natsConn.Publish(nats.CameraAlertsSubject(r.CameraId), aProto)
				if err != nil {
					logrus.WithError(err).Error("failed to publish alert to nats")
					continue
				}

				// Заносим лицо в кеш активных лиц.
				activeFacesMx.Lock()
				activeFaces[face.PersonID] = time.Now()
				activeFacesMx.Unlock()
			}
		}(cameraID)
	}

	logrus.Info("alerter started")

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

	err = db.Close()
	if err != nil {
		logrus.WithError(err).Error("failed to close db")
	}

	logrus.Info("everything is stopped, exiting")
}
