package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"io"
	"os"
)

func deriveKey(filename string) []byte {

	sum := sha256.Sum256([]byte(filename))
	return sum[:]

}

func EncryptAndSave(secretKey, filepath string, data map[string]string) error {

	key := deriveKey(secretKey)
	plaintext, err := json.Marshal(data)
	if err != nil {
		return err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return err
	}
	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)

	return os.WriteFile(filepath, ciphertext, 0600)

}

func LoadAndDecrypt(secretKey, filepath string) (map[string]string, error) {

	key := deriveKey(secretKey)
	ciphertext, err := os.ReadFile(filepath)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonceSize := gcm.NonceSize()
	nonce, data := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, data, nil)
	if err != nil {
		return nil, err
	}
	var result map[string]string
	err = json.Unmarshal(plaintext, &result)

	return result, err

}
