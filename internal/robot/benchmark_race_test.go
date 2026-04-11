//go:build race

package robot

func init() {
	raceDetectorEnabled = true
}
