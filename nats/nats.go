package nats

import "fmt"

const cameraDetectionsSubjectFormat = "camera.%s.detections"

func CameraDetectionsSubject(cameraID string) string {
	return fmt.Sprintf(cameraDetectionsSubjectFormat, cameraID)
}

const cameraRecognitionsSubjectFormat = "camera.%s.recognitions"

func CameraRecognitionsSubject(cameraID string) string {
	return fmt.Sprintf(cameraRecognitionsSubjectFormat, cameraID)
}
