package helpers

import (
	"os"
	"strings"
)

func RemoveDoaminError(url string) bool {
	if url == os.Getenv("DOMAIN") {
		return false
	}

	newUrl := strings.Replace(url, "http://", "", 1)
	newUrl = strings.Replace(newUrl, "https://", "", 1)
	newUrl = strings.Replace(newUrl, "/", "", 1)
	newUrl = strings.Replace(newUrl, "www.", "", 1)

	if newUrl == os.Getenv("DOMAIN") {
		return false
	}
	return true
}

func EnforceHTTP(url string) string {
	if len(url) <= 4 || url[:4] != "http" {
		url = "http://" + url
	}
	return url
}
