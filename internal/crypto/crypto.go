// Package crypto cifra secretos en reposo con AES-256-GCM y la clave maestra.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
)

var ErrInvalidCiphertext = errors.New("texto cifrado inválido o clave incorrecta")

// Box cifra y descifra con una clave fija de 32 bytes.
type Box struct {
	aead cipher.AEAD
}

// NewBox exige una clave de 32 bytes (AES-256).
func NewBox(key []byte) (*Box, error) {
	if len(key) != 32 {
		return nil, errors.New("la clave maestra debe tener 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Seal devuelve nonce || ciphertext || tag. Cada llamada usa un nonce aleatorio.
func (b *Box) Seal(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return b.aead.Seal(nonce, nonce, plaintext, nil), nil
}

// Open invierte Seal. Falla si el dato fue alterado o la clave no coincide.
func (b *Box) Open(data []byte) ([]byte, error) {
	ns := b.aead.NonceSize()
	if len(data) < ns+b.aead.Overhead() {
		return nil, ErrInvalidCiphertext
	}
	plain, err := b.aead.Open(nil, data[:ns], data[ns:], nil)
	if err != nil {
		return nil, ErrInvalidCiphertext
	}
	return plain, nil
}
