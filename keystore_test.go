package iceroot

import (
	"context"
	"errors"
	"testing"
)

func TestKeystoreMemoryCeiling(t *testing.T) {
	s := testSDK(t)
	ctx := context.Background()
	const ceiling uint32 = 19456
	for _, tc := range []struct {
		name       string
		storedKib  uint32
		newKib     uint32
		maxKib     uint32
		wantRefuse bool
	}{
		{"both above ceiling", 20480, 20480, ceiling, true},
		{"stored above ceiling", 20480, ceiling, ceiling, true},
		{"new above ceiling", ceiling, 20480, ceiling, true},
		{"at ceiling", ceiling, ceiling, ceiling, false},
		{"below ceiling", ceiling, ceiling, 20480, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stored, err := s.EncryptKeystore(ctx, []byte(phrase), []byte("password"), KeystoreParams{MemoryKib: tc.storedKib, Iterations: 2, Parallelism: 1})
			if err != nil {
				t.Fatal(err)
			}
			params := KeystoreParams{MemoryKib: tc.newKib, Iterations: 2, Parallelism: 1}
			for _, changePassword := range []bool{true, false} {
				name := "ReencryptKeystore"
				if changePassword {
					name = "ChangeKeystorePassword"
				}
				t.Run(name, func(t *testing.T) {
					password := "password"
					var changed []byte
					var err error
					if changePassword {
						changed, err = s.ChangeKeystorePassword(ctx, stored, []byte(password), []byte("new password"), params, tc.maxKib)
						password = "new password"
					} else {
						changed, err = s.ReencryptKeystore(ctx, stored, []byte(password), params, tc.maxKib)
					}
					if tc.wantRefuse {
						var e *Error
						if !errors.As(err, &e) || e.Code != "ParamsOutOfRange" {
							t.Fatalf("want ParamsOutOfRange, got %v", err)
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					account, err := s.FromKeystore(ctx, Devnet(), changed, []byte(password), AccountOptions{}, tc.maxKib)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = account.Release(ctx) })
				})
			}
		})
	}
}
