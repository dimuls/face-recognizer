// Небольшая программа для ручного тестирования детектора лиц. Ничего
// особенного: мы просто получаем обнаружения с соответствующего камере канала
// в nats и выводим их в окне.

package main

import (
	"context"
	"database/sql"
	"flag"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"syscall"

	"github.com/lib/pq"
	natsGo "github.com/nats-io/nats.go"
	"github.com/sirupsen/logrus"
	"gocv.io/x/gocv"
	"google.golang.org/protobuf/proto"

	"github.com/dimuls/face-recognizer/entity"
	"github.com/dimuls/face-recognizer/nats"
)

func main() {
	var (
		natsURL     string
		cameraID    string
		personID    int64
		personName  string
		postgresDSN string
		facesToAdd  int
	)

	flag.StringVar(&natsURL, "nats-url", "nats://127.0.0.1:4222", "nats server url")
	flag.StringVar(&cameraID, "camera-id", "*", "camera id")
	flag.Int64Var(&personID, "person-id", 0, "person id")
	flag.StringVar(&personName, "person-name", "", "person name")
	flag.StringVar(&postgresDSN, "postgres-dsn", "", "postgres dsn")
	flag.IntVar(&facesToAdd, "faces-to-add", 100, "faces to add to db")

	flag.Parse()

	db, err := sql.Open("postgres", postgresDSN)
	if err != nil {
		logrus.WithError(err).Fatal("failed to open db")
	}

	if personID == 0 {
		if personName == "" {
			logrus.Fatal("empty person name")
		}
		err = db.QueryRow(`
			insert into person (name) values ($1)
			returning id
		`, personName).Scan(&personID)
		if err != nil {
			logrus.WithError(err).Fatal("failed to add person to DB")
		}
		logrus.WithField("person_id", personID).Info("created person")
	}

	nc, err := natsGo.Connect(natsURL)
	if err != nil {
		logrus.WithError(err).Fatal("failed to connect to nats")
	}

	sub, err := nc.SubscribeSync(nats.CameraRecognitionsSubject(cameraID))
	if err != nil {
		logrus.WithError(err).Fatal("failed to nats subscribe to recognitions")
	}

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()

		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		w := gocv.NewWindow(cameraID)
		defer w.Close()

		for {
			if facesToAdd == 0 {
				cancel()
			}

			select {
			case <-ctx.Done():
				return
			default:
			}

			msg, err := sub.NextMsgWithContext(ctx)
			if err != nil {
				continue
			}

			r := &entity.Recognition{}

			err = proto.Unmarshal(msg.Data, r)
			if err != nil {
				logrus.WithError(err).Error("failed to proto unmarshal recognition")
				return
			}

			img, err := gocv.IMDecode(r.Face, gocv.IMReadUnchanged)
			if err != nil {
				logrus.WithError(err).Error("failed to decode frame image")
				return
			}

			w.IMShow(img)

			_, err = db.Exec(`
				insert into face (person_id, descriptor) 
				values ($1, $2)
			`, personID, pq.Array(r.FaceDescriptor))
			if err != nil {
				logrus.WithError(err).Error("failed to add face to db")
			}

			facesToAdd--

			if w.WaitKey(1) == 27 {
				cancel()
			}

			err = img.Close()
			if err != nil {
				logrus.WithError(err).Error("failed to close frame image")
			}
		}
	}()

	exit := make(chan os.Signal)
	signal.Notify(exit, os.Interrupt, syscall.SIGTERM)

	select {
	case <-exit:
	case <-ctx.Done():
	}

	cancel()
	wg.Wait()

	err = sub.Unsubscribe()
	if err != nil {
		logrus.Info("failed to nats unsubscribe")
	}

	nc.Close()
}
