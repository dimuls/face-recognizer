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
	var configPath string

	flag.StringVar(&configPath, "conf", "config.yaml", "config path")
	flag.Parse()

	config, err := LoadConfig(configPath)
	if err != nil {
		logrus.WithError(err).Fatal("failed to load config")
	}

	err = os.Setenv("CUDA_VISIBLE_DEVICES", strconv.Itoa(config.CudaDeviceID))
	if err != nil {
		logrus.WithError(err).Fatal("failed to set cuda device id")
	}

	if config.FrameRate == 0 {
		config.FrameRate = 25
	}

	detector, err := face.NewDetector(config.ModelPath)
	if err != nil {
		logrus.WithError(err).Fatal("failed to create detector")
	}

	natsConn, err := natsGo.Connect(config.NatsURL)
	if err != nil {
		logrus.WithError(err).Fatal("failed to connect to nats")
	}

	frames := map[string]<-chan []byte{}
	ctx, cancel := context.WithCancel(context.Background())

	var wg sync.WaitGroup
	for _, camera := range config.Cameras {
		wg.Add(1)
		go func(camera CameraConfig) {
			defer wg.Done()
			frames[camera.ID] = runCameraProcessor(ctx, camera,
				config.ClosedDuration)
		}(camera)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		runDetector(ctx, detector, natsConn, config.FrameRate, frames)
	}()

	exit := make(chan os.Signal)
	signal.Notify(exit, os.Interrupt, syscall.SIGTERM)

	<-exit

	cancel()
	wg.Wait()
}

func runCameraProcessor(ctx context.Context, camera CameraConfig,
	closedDuration time.Duration) <-chan []byte {

	var (
		wg     sync.WaitGroup
		frames = make(chan []byte)
	)

	wg.Add(1)
	go func() {
		defer wg.Done()

		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		var (
			err    error
			stream *gocv.VideoCapture

			successTime = time.Now()
			readTries   int

			image = gocv.NewMat()
		)

	loop:
		for {
			select {
			case <-ctx.Done():
				break loop
			default:
			}

			if stream == nil {
				stream, err = gocv.OpenVideoCapture(camera.URL)
				if err != nil {
					logrus.WithError(err).Error("failed to open stream")
					time.Sleep(time.Second)
					continue
				}
			}

			if readTries > 0 && time.Now().Sub(successTime) > closedDuration {
				logrus.Warning("failed to read too long, reconnecting")
				err = stream.Close()
				if err != nil {
					logrus.WithError(err).Error("failed to close stream")
				}
				stream = nil
				successTime = time.Now()
				readTries = 0
				continue
			}

			if !stream.Read(&image) {
				readTries++
				time.Sleep(time.Second)
				continue
			}

			frame, err := gocv.IMEncode(gocv.JPEGFileExt, image)
			if err != nil {
				logrus.WithError(err).Error("failed to JPEG encode image")
				continue
			}

			select {
			case frames <- frame:
			default:
			}

			successTime = time.Now()
			readTries = 0
		}

		if stream == nil {
			err = stream.Close()
			if err != nil {
				logrus.WithError(err).Error("failed to close stream")
			}
		}
	}()

	wg.Wait()

	return frames
}

func runDetector(ctx context.Context, detector *face.Detector, natsConn *natsGo.Conn,
	frameRate int, cameraFramesChans map[string]<-chan []byte) {

	var (
		wg                sync.WaitGroup
		cameraFramesMap   = make(map[string][]byte, len(cameraFramesChans))
		cameraFramesMapMx sync.Mutex
	)

	for cameraID, cameraFramesChan := range cameraFramesChans {
		wg.Add(1)
		go func(cameraID string, cameraFramesChan <-chan []byte) {
			defer wg.Done()
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

	var (
		currentCameraFramesMap map[string][]byte

		frames       = make([][]byte, 0, len(cameraFramesChans))
		images       []gocv.Mat
		imageIndexes = make(map[string]int, len(cameraFramesChans))

		frameTime = time.Second / time.Duration(frameRate)
		startTime time.Time
	)

	t := time.NewTimer(frameTime)
	defer t.Stop()

loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		default:
		}

		if time.Now().Sub(startTime) < frameTime {
			if !t.Stop() {
				<-t.C
			}
			t.Reset(time.Now().Sub(startTime))
			<-t.C
		}

		startTime = time.Now()

		cameraFramesMapMx.Lock()
		currentCameraFramesMap = cameraFramesMap
		cameraFramesMap = make(map[string][]byte, len(cameraFramesChans))
		cameraFramesMapMx.Unlock()

		for cameraID, frame := range currentCameraFramesMap {
			image, err := gocv.IMDecode(frame, gocv.IMReadUnchanged)
			if err != nil {
				logrus.WithError(err).Error("failed to decode frame image")
			}
			frames = append(frames, frame)
			images = append(images, image)
			imageIndexes[cameraID] = len(images) - 1
		}
		if len(images) == 0 {
			continue
		}

		detections, err := detector.BatchDetect(images)
		if err != nil {
			logrus.WithError(err).Error("failed to batch detect")
		}

		detectedAt := time.Now()

		for cameraID, i := range imageIndexes {
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

			dsProto, err := proto.Marshal(&entity.Detections{
				CameraId:   cameraID,
				Frame:      frames[i],
				Detection:  ds,
				DetectedAt: timestamppb.New(detectedAt),
			})
			if err != nil {
				logrus.WithError(err).Error(
					"failed to proto marshal detections")
			} else {
				err = natsConn.Publish(nats.DetectionsSubject(cameraID), dsProto)
				if err != nil {
					logrus.WithError(err).Error(
						"failed to publish detections")
				}
			}
		}

		frames = frames[:0]

		for _, image := range images {
			err := image.Close()
			if err != nil {
				logrus.WithError(err).Error("failed to close frame image")
			}
		}

		images = images[:0]

		for cameraID := range imageIndexes {
			delete(imageIndexes, cameraID)
		}
	}

	wg.Wait()
}
