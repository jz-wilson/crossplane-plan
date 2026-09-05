package detector

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestAnnotationDetector_DetectPR(t *testing.T) {
	tests := []struct {
		name        string
		annotations map[string]string
		expectedPR  int
	}{
		{
			name: "valid PR number in annotation",
			annotations: map[string]string{
				"millstone.tech/preview-pr": "789",
			},
			expectedPR: 789,
		},
		{
			name: "no PR annotation",
			annotations: map[string]string{
				"kubectl.kubernetes.io/last-applied-configuration": "{}",
			},
			expectedPR: 0,
		},
		{
			name:        "nil annotations",
			annotations: nil,
			expectedPR:  0,
		},
		{
			name: "invalid PR number",
			annotations: map[string]string{
				"millstone.tech/preview-pr": "not-a-number",
			},
			expectedPR: 0,
		},
		{
			name: "multiple annotations",
			annotations: map[string]string{
				"millstone.tech/preview-pr": "321",
				"app":                       "test",
			},
			expectedPR: 321,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			detector := NewAnnotationDetector()
			xr := &unstructured.Unstructured{}
			if tt.annotations != nil {
				xr.SetAnnotations(tt.annotations)
			}

			got := detector.DetectPR(xr)
			if got != tt.expectedPR {
				t.Errorf("DetectPR() = %d, want %d", got, tt.expectedPR)
			}
		})
	}
}

func TestAnnotationDetectorWithKey_DetectPR(t *testing.T) {
	customKey := "example.com/pr-id"
	detector := NewAnnotationDetectorWithKey(customKey)

	xr := &unstructured.Unstructured{}
	xr.SetAnnotations(map[string]string{
		customKey: "654",
	})

	got := detector.DetectPR(xr)
	if got != 654 {
		t.Errorf("DetectPR() with custom key = %d, want 654", got)
	}
}
