package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Параметры argon2id — вторая рекомендация RFC 9106 (§4) для систем с
// ограниченной памятью: 64 МиБ, три прохода. Вход — редкая операция у
// десятка пользователей, и сотня миллисекунд на неё ничего не стоит, а
// перебор по утёкшему хешу дорожает ровно во столько же раз.
const (
	argonTime    = 3
	argonMemory  = 64 * 1024 // КиБ
	argonThreads = 4
	argonKeyLen  = 32
	saltLen      = 16
)

var errBadHash = errors.New("неизвестный формат хеша пароля")

// HashPassword считает хеш в PHC-формате:
//
//	$argon2id$v=19$m=65536,t=3,p=4$<соль>$<хеш>
//
// Параметры лежат в самой строке, поэтому их можно поменять позже, не
// ломая старые хеши: VerifyPassword читает параметры из хеша, а не из констант.
func HashPassword(password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("salt: %w", err)
	}

	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)

	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// VerifyPassword сверяет пароль с хешем. Сравнение — за постоянное время:
// иначе по времени ответа можно было бы подбирать хеш побайтно.
func VerifyPassword(hash, password string) (bool, error) {
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errBadHash
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errBadHash
	}

	var (
		memory  uint32
		time    uint32
		threads uint8
	)
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return false, errBadHash
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, errBadHash
	}

	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, errBadHash
	}

	got := argon2.IDKey([]byte(password), salt, time, memory, threads, uint32(len(want)))

	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
