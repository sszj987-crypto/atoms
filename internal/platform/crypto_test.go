package platform

import "testing"

func TestPasswordRoundTrip(t *testing.T) {
	hash, err := hashPassword("correct horse battery staple")
	if err != nil || !verifyPassword(hash, "correct horse battery staple") || verifyPassword(hash, "wrong") {
		t.Fatal("password verification failed")
	}
}

func TestSecretRoundTrip(t *testing.T) {
	key := make([]byte, 32)
	key[0] = 1
	sealed, err := encrypt(key, "secret")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := decrypt(key, sealed)
	if err != nil || plain != "secret" {
		t.Fatal("secret round trip failed")
	}
}
