package federation

import (
	"testing"
)

func TestPadPayload(t *testing.T) {
	buckets := []int{1024, 4096, 16384, 65536}

	tests := []struct {
		name       string
		payload    []byte
		wantLen    int
		wantPrefix bool
	}{
		{
			name:       "small payload",
			payload:    []byte("hello world"),
			wantLen:    1024,
			wantPrefix: true,
		},
		{
			name:       "exactly at bucket",
			payload:    make([]byte, 1024),
			wantLen:    1024,
			wantPrefix: true,
		},
		{
			name:       "between first two buckets",
			payload:    make([]byte, 2000),
			wantLen:    4096,
			wantPrefix: true,
		},
		{
			name:       "empty payload",
			payload:    []byte{},
			wantLen:    1024,
			wantPrefix: false, // empty has no prefix to preserve
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := make([]byte, len(tt.payload))
			copy(original, tt.payload)

			padded := PadPayload(tt.payload, buckets)

			if len(padded) != tt.wantLen {
				t.Errorf("PadPayload() len = %d, want %d", len(padded), tt.wantLen)
			}

			if tt.wantPrefix {
				// Check original prefix is preserved
				for i := 0; i < len(original); i++ {
					if padded[i] != original[i] {
						t.Errorf("PadPayload() prefix not preserved at index %d", i)
						break
					}
				}
			}
		})
	}
}

func TestPadPayloadDisabled(t *testing.T) {
	// When padding is disabled (nil or empty buckets), payload should be unchanged
	payload := []byte("test payload")

	result := PadPayload(payload, nil)

	if string(result) != string(payload) {
		t.Error("Expected payload to be unchanged with nil buckets")
	}

	result = PadPayload(payload, []int{})

	if string(result) != string(payload) {
		t.Error("Expected payload to be unchanged with empty buckets")
	}
}

func TestPadPayloadBucketOrdering(t *testing.T) {
	// Test that buckets are sorted correctly (smallest to largest)
	buckets := []int{65536, 1024, 65536, 4096} // Unsorted
	payload := []byte("test")

	padded := PadPayload(payload, buckets)

	// Should still work correctly - should find nearest bucket >= len(payload)
	if len(padded) < 1024 {
		t.Errorf("Padded length %d is too small", len(padded))
	}
}
