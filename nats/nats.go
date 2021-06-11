package nats

import "fmt"

const detectionsSubjectFormat = "camera.%s.detections"

func DetectionsSubject(cameraID string) string {
	return fmt.Sprintf(detectionsSubjectFormat, cameraID)
}
