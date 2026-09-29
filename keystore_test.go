package iceroot

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// TestKeystoreMemoryCeiling checks that a password change and a re-encryption refuse a stored
// keystore, or new parameters, that ask for more memory than the caller's ceiling, and that the
// caller's own ceiling reaches the core: parameters above the format's floor are accepted at a
// ceiling that admits them, and a ceiling of 0 is the floor, not the absence of one.
func TestKeystoreMemoryCeiling(t *testing.T) {
	s := testSDK(t)
	ctx := context.Background()
	const floor, above uint32 = 19456, 20480
	stored := map[uint32][]byte{}
	for _, kib := range []uint32{floor, above} {
		keystore, err := s.EncryptKeystore(ctx, []byte(phrase), []byte("password"), KeystoreParams{MemoryKib: kib, Iterations: 2, Parallelism: 1})
		if err != nil {
			t.Fatal(err)
		}
		stored[kib] = keystore
	}
	for _, tc := range []struct {
		name      string
		storedKib uint32
		newKib    uint32
		maxKib    uint32
		refused   bool
	}{
		{"both above the ceiling", above, above, floor, true},
		{"stored above the ceiling", above, floor, floor, true},
		{"new above the ceiling", floor, above, floor, true},
		{"a ceiling of 0 is the floor", floor, above, 0, true},
		{"at the ceiling", above, above, above, false},
		{"under the ceiling", floor, floor, above, false},
	} {
		params := KeystoreParams{MemoryKib: tc.newKib, Iterations: 2, Parallelism: 1}
		for _, function := range []string{"ChangeKeystorePassword", "ReencryptKeystore"} {
			t.Run(tc.name+"/"+function, func(t *testing.T) {
				var changed []byte
				var err error
				if function == "ChangeKeystorePassword" {
					changed, err = s.ChangeKeystorePassword(ctx, stored[tc.storedKib], []byte("password"), []byte("new password"), params, tc.maxKib)
				} else {
					changed, err = s.ReencryptKeystore(ctx, stored[tc.storedKib], []byte("password"), params, tc.maxKib)
				}
				if tc.refused {
					var e *Error
					if !errors.As(err, &e) || e.Code != "ParamsOutOfRange" {
						t.Fatalf("want ParamsOutOfRange, got %v", err)
					}
					var details struct {
						Param   string `json:"param"`
						Value   uint32 `json:"value"`
						Maximum uint32 `json:"maximum"`
					}
					if err := json.Unmarshal(e.Details, &details); err != nil {
						t.Fatal(err)
					}
					if details.Param != "memory" || details.Value != above || details.Maximum != floor {
						t.Fatalf("want memory %d over a ceiling of %d, got %s", above, floor, e.Details)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				header, err := s.InspectKeystore(ctx, changed)
				if err != nil {
					t.Fatal(err)
				}
				var written KeystoreParams
				if err := json.Unmarshal(header, &written); err != nil {
					t.Fatal(err)
				}
				if written != params {
					t.Fatalf("want %+v written, got %+v", params, written)
				}
			})
		}
	}
}
