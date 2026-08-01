package store

import (
	"strings"
	"testing"
)

// boolPtr is a small helper for building presets with explicit boolean values.
func boolPtr(b bool) *bool { return &b }

func TestValidatePresetDefaultNetworksMustBeSelectable(t *testing.T) {
	cases := []struct {
		name    string
		preset  *ImagePreset
		wantErr string // substring; empty means no error expected
	}{
		{
			name:    "empty pool allows any default",
			preset:  &ImagePreset{SelectableNetworks: nil, Networks: []string{"mudp_alice_net"}},
			wantErr: "",
		},
		{
			name:    "default inside pool passes",
			preset:  &ImagePreset{SelectableNetworks: []string{"mudp_alice_net", "bridge"}, Networks: []string{"mudp_alice_net"}},
			wantErr: "",
		},
		{
			name:    "default outside pool rejected",
			preset:  &ImagePreset{SelectableNetworks: []string{"mudp_alice_net"}, Networks: []string{"mudp_bob_net"}},
			wantErr: "must be within the selectable networks list",
		},
		{
			name:    "whitespace around names is ignored",
			preset:  &ImagePreset{SelectableNetworks: []string{"  mudp_alice_net  "}, Networks: []string{"mudp_alice_net"}},
			wantErr: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePreset(tc.preset)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestIsEmptyPresetSelectableNetworks(t *testing.T) {
	// A preset that carries only a selectable pool (no other fields) is still a
	// real preset and must not be treated as empty — otherwise it would be
	// dropped on save and the restriction silently lost.
	if !isEmptyPreset(&ImagePreset{SelectableNetworks: []string{"mudp_alice_net"}}) {
		// expected: not empty
	} else {
		t.Fatal("preset with only SelectableNetworks should not be considered empty")
	}
	if !isEmptyPreset(&ImagePreset{}) {
		t.Fatal("zero-value preset should be considered empty")
	}
}

func TestEncodeDecodePresetSelectableNetworks(t *testing.T) {
	original := &ImagePreset{
		SelectableNetworks: []string{"mudp_alice_net", "bridge"},
		Networks:           []string{"mudp_alice_net"},
		Ports:              []string{"8080"},
		RequireLogin:       boolPtr(true),
	}
	encoded, err := EncodePreset(original)
	if err != nil {
		t.Fatalf("EncodePreset: %v", err)
	}
	if encoded == "" {
		t.Fatal("expected non-empty encoded preset")
	}
	decoded, err := DecodePreset(encoded)
	if err != nil {
		t.Fatalf("DecodePreset: %v", err)
	}
	if decoded == nil {
		t.Fatal("decoded preset is nil")
	}
	if len(decoded.SelectableNetworks) != 2 {
		t.Fatalf("expected 2 selectable networks, got %v", decoded.SelectableNetworks)
	}
	if len(decoded.Networks) != 1 || decoded.Networks[0] != "mudp_alice_net" {
		t.Fatalf("unexpected default networks: %v", decoded.Networks)
	}
}
