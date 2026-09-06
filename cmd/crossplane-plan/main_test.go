package main

import (
	"testing"

	"github.com/millstonehq/crossplane-plan/pkg/config"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestCreateDetectorUsesConfiguredMetadataKey(t *testing.T) {
	tests := []struct {
		name          string
		strategy      string
		labelKey      string
		annotationKey string
		setMetadata   func(*unstructured.Unstructured)
		wantPR        int
	}{
		{
			name:     "label",
			strategy: "label",
			labelKey: "example.com/pull-request",
			setMetadata: func(xr *unstructured.Unstructured) {
				xr.SetLabels(map[string]string{"example.com/pull-request": "123"})
			},
			wantPR: 123,
		},
		{
			name:          "annotation",
			strategy:      "annotation",
			annotationKey: "example.com/pull-request",
			setMetadata: func(xr *unstructured.Unstructured) {
				xr.SetAnnotations(map[string]string{"example.com/pull-request": "456"})
			},
			wantPR: 456,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			detector, err := createDetector(&config.Config{
				DetectionStrategy: tt.strategy,
				LabelKey:          tt.labelKey,
				AnnotationKey:     tt.annotationKey,
			})
			if err != nil {
				t.Fatalf("createDetector() error = %v", err)
			}

			xr := &unstructured.Unstructured{}
			tt.setMetadata(xr)
			if got := detector.DetectPR(xr); got != tt.wantPR {
				t.Errorf("DetectPR() = %d, want %d", got, tt.wantPR)
			}
		})
	}
}
