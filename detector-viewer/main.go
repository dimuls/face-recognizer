// Небольшая программа для ручного тестирования детектора лиц. Ничего
// особенного: мы просто получаем обнаружения с соответствующего камере канала
// в nats и выводим их в окне.

package main

import (
	"context"
	"flag"
	"fmt"
	"image"
	"image/color"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"syscall"

	natsGo "github.com/nats-io/nats.go"
	"github.com/sirupsen/logrus"
	"gocv.io/x/gocv"
	"google.golang.org/protobuf/proto"

	"github.com/dimuls/face-recognizer/entity"
	"github.com/dimuls/face-recognizer/nats"
)

var blue = color.RGBA{
	R: 0,
	G: 0,
	B: 255,
	A: 0,
}

func main() {
	var (
		natsURL  string
		cameraID string
	)

	flag.StringVar(&natsURL, "nats-url", "nats://127.0.0.1:4222", "nats server url")
	flag.StringVar(&cameraID, "camera-id", "", "camera id")

	flag.Parse()

	nc, err := natsGo.Connect(natsURL)
	if err != nil {
		logrus.WithError(err).Fatal("failed to connect to nats")
	}

	sub, err := nc.SubscribeSync(nats.CameraDetectionsSubject(cameraID))
	if err != nil {
		logrus.WithError(err).Fatal("failed to nats subscribe to detections")
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
			select {
			case <-ctx.Done():
				return
			default:
			}

			msg, err := sub.NextMsgWithContext(ctx)
			if err != nil {
				logrus.WithError(err).Error("get next nuts message")
				continue
			}

			ds := &entity.Detections{}

			err = proto.Unmarshal(msg.Data, ds)
			if err != nil {
				logrus.WithError(err).Error("failed to proto unmarshal detections")
				return
			}

			img, err := gocv.IMDecode(ds.Frame, gocv.IMReadUnchanged)
			if err != nil {
				logrus.WithError(err).Error("failed to decode frame image")
				return
			}

			for _, d := range ds.Detections {
				gocv.Rectangle(&img, image.Rectangle{
					Min: image.Point{
						X: int(d.MinX),
						Y: int(d.MinY),
					},
					Max: image.Point{
						X: int(d.MaxX),
						Y: int(d.MaxY),
					},
				}, blue, 1)
				gocv.PutText(&img, fmt.Sprintf("%.2f", d.Confidence),
					image.Point{
						X: int(d.MinX),
						Y: int(d.MinY),
					}, gocv.FontHersheyComplex, 1, blue, 1)
			}

			w.IMShow(img)

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

	<-exit

	cancel()
	wg.Wait()

	err = sub.Unsubscribe()
	if err != nil {
		logrus.Info("failed to nats unsubscribe")
	}

	nc.Close()
}
