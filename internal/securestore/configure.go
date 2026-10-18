package securestore

import securestoreserializer "github.com/dujiao-next/internal/securestore/serializer"

func Configure(secret string) error {
	return securestoreserializer.Configure(secret)
}
