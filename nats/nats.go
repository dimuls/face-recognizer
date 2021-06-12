package nats

import "fmt"

const detectionsSubjectFormat = "camera.%s.detections"

func CameraDetectionsSubject(cameraID string) string {
	return fmt.Sprintf(detectionsSubjectFormat, cameraID)
}
